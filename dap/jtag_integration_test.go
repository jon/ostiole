//go:build integration

package dap

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/ftdi"
	ftdidiscovery "github.com/jon/ostiole/ftdi/discovery"
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

func TestHILZCU104JTAGDP(t *testing.T) {
	if os.Getenv("OSTIOLE_ZCU104_JTAGDP_HIL") != "1" {
		t.Skip("set OSTIOLE_ZCU104_JTAGDP_HIL=1 for FTDI 01691 port A and the enabled ZCU104 chain")
	}
	for _, discovered := range []bool{false, true} {
		name := "direct"
		if discovered {
			name = "discovered"
		}
		if !t.Run(name, func(t *testing.T) {
			var expectedPower uint32
			var expectedOverrun bool
			for session := range 2 {
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				power, overrun := exerciseJTAGDPMemory(t, ctx, discovered)
				cancel()
				if t.Failed() {
					return
				}
				if session != 0 && (power != expectedPower || overrun != expectedOverrun) {
					t.Fatalf("fresh session inherited different control state: acquired power %#x/%#x, ORUNDETECT %v/%v", power, expectedPower, overrun, expectedOverrun)
				}
				expectedPower, expectedOverrun = power, overrun
			}
		}) {
			return
		}
	}
}

func exerciseJTAGDPMemory(t *testing.T, ctx context.Context, discovered bool) (uint32, bool) {
	t.Helper()
	wire, closeOwner := openJTAGDPBench(t, ctx, discovered)
	arm, _ := jtag.IDCODE(4, 0x5ba00477)
	xilinx, _ := jtag.IDCODE(12, 0x14730093)
	chain, err := jtag.NewChain(jtag.New(wire), jtag.Layout{arm, xilinx})
	if err != nil {
		t.Fatal(errors.Join(err, closeOwner()))
	}
	dp := NewDebugPort(JTAGDP(chain, 0), WithMaxWaits(100))
	var mem *MemAP
	defer func() {
		for range 3 {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err = mem.Release(cleanup)
			if err == nil {
				err = dp.Release(cleanup)
			}
			cancel()
			if err == nil {
				break
			}
			if errors.Is(err, ftdi.ErrChannelPoisoned) {
				break
			}
		}
		if err != nil {
			t.Errorf("stopping without closing the probe after failed DAP cleanup: %v", err)
			return
		}
		for range 3 {
			if err = closeOwner(); err == nil {
				return
			}
		}
		t.Errorf("close probe: %v", err)
	}()
	identity, err := dp.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := identity.IDCODE()
	power, overrun := dp.state.ownedPower, dp.jtag.ownedOverrun
	t.Logf("JTAG-DP IDCODE=%#08x acquired power=%#08x inherited ORUNDETECT=%v", id, power, overrun)
	sel := NewAPSel(1)
	idr, err := dp.ReadAPIDR(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AP1 IDR=%#08x", idr.Raw)
	saved := make(map[uint8]uint32)
	for _, addr := range []uint8{0, 4} {
		saved[addr], err = dp.ReadRawAP(ctx, sel.Address(uint16(addr)))
		if err != nil {
			t.Fatal(err)
		}
	}
	mem, err = OpenMemAP(ctx, dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	var cid [4]uint32
	for i := range cid {
		cid[i], err = mem.ReadWord(ctx, 0x80410ff0+uint32(i)*4)
		if err != nil {
			t.Fatal(err)
		}
	}
	if cid[0]&0xff != 0x0d || cid[1]&0x0f != 0 || cid[2]&0xff != 5 || cid[3]&0xff != 0xb1 {
		t.Fatalf("unexpected CoreSight component ID: %08x", cid)
	}
	t.Logf("AP1 component words at 0x80410ff0: %08x", cid)
	if err := mem.Release(ctx); err != nil {
		t.Fatal(err)
	}
	for addr, want := range saved {
		got, err := dp.ReadRawAP(ctx, sel.Address(uint16(addr)))
		if err != nil || got != want {
			t.Fatalf("restored AP1 register %#x = %#08x, expected %#08x: %v", addr, got, want, err)
		}
	}
	t.Logf("AP1 CSW/TAR restored: %#08x/%#08x", saved[0], saved[4])
	return power, overrun
}

func openJTAGDPBench(t *testing.T, ctx context.Context, discovered bool) (jtag.Wire, func() error) {
	t.Helper()
	if discovered {
		var registry discover.Registry
		if err := ftdidiscovery.Register(&registry); err != nil {
			t.Fatal(err)
		}
		inventory, err := registry.Probes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := inventory.Select(discover.Selection{Provider: ftdidiscovery.ID, Serial: "01691", Function: "A"})
		if errors.Is(err, discover.ErrCandidateNotFound) || errors.Is(err, discover.ErrCandidateAmbiguous) {
			t.Skip(err)
		}
		if err != nil {
			t.Fatal(err)
		}
		owner, err := candidate.Open(ctx)
		if err != nil {
			if owner != nil {
				err = errors.Join(err, owner.Close())
			}
			t.Fatal(err)
		}
		wire, err := owner.JTAG(ctx, probe.JTAGConfig{MaxClockHz: 100_000})
		if err != nil {
			t.Fatal(errors.Join(err, owner.Close()))
		}
		return wire, owner.Close
	}
	bus := usb.New()
	devices, err := bus.List(ctx, []usb.DeviceFilter{usb.ExactDevice(ftdi.VID, ftdi.PIDFT4232H)})
	if err != nil {
		t.Fatal(err)
	}
	var matches []usb.DeviceInfo
	for _, dev := range devices {
		if dev.Serial == "01691" {
			matches = append(matches, dev)
		}
	}
	if len(matches) != 1 {
		t.Skipf("require one FT4232H 01691; found %d", len(matches))
	}
	device, err := bus.Open(ctx, matches[0])
	if err != nil {
		t.Fatal(err)
	}
	channel, err := ftdi.Open(ctx, device, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 100_000})
	if err != nil {
		closeOwner := device.Close
		if channel != nil {
			closeOwner = channel.Close
		}
		t.Fatal(errors.Join(err, closeOwner()))
	}
	return channel, channel.Close
}
