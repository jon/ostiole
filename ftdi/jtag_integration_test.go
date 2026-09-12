//go:build integration

package ftdi_test

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

func TestHILFT4232HProbeJTAG(t *testing.T) {
	serial := os.Getenv("OSTIOLE_JTAG_SERIAL")
	if serial == "" || os.Getenv("OSTIOLE_ZCU104_JTAG_HIL") != "1" {
		t.Skip("set OSTIOLE_ZCU104_JTAG_HIL=1 and OSTIOLE_JTAG_SERIAL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var registry discover.Registry
	if err := ftdidiscovery.Register(&registry); err != nil {
		t.Fatal(err)
	}
	inventory, err := registry.Probes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := inventory.Select(discover.Selection{Provider: ftdidiscovery.ID, Serial: serial, Function: "A"})
	if errors.Is(err, discover.ErrCandidateNotFound) || errors.Is(err, discover.ErrCandidateAmbiguous) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	owner, err := candidate.Open(ctx)
	released := true
	if owner != nil {
		defer func() {
			if !released {
				t.Error("retaining probe after failed chain cleanup")
				return
			}
			for range 3 {
				if err = owner.Close(); err == nil {
					return
				}
			}
			t.Errorf("close probe: %v", err)
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
	wire, err := owner.JTAG(ctx, probe.JTAGConfig{MaxClockHz: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	exerciseJTAGChain(t, ctx, wire, &released)
}

// TestHILFT4232HJTAG clocks the explicitly enabled ZCU104 chain. Board-specific
// DAP activation must already be complete; this test does not configure it.
func TestHILFT4232HJTAG(t *testing.T) {
	serial := os.Getenv("OSTIOLE_JTAG_SERIAL")
	if serial == "" || os.Getenv("OSTIOLE_ZCU104_JTAG_HIL") != "1" {
		t.Skip("set OSTIOLE_ZCU104_JTAG_HIL=1 and OSTIOLE_JTAG_SERIAL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	bus := usb.New()
	devices, err := bus.List(ctx, []usb.DeviceFilter{usb.ExactDevice(ftdi.VID, ftdi.PIDFT4232H)})
	if err != nil {
		t.Fatal(err)
	}
	var matches []usb.DeviceInfo
	for _, device := range devices {
		if device.Serial == serial {
			matches = append(matches, device)
		}
	}
	if len(matches) != 1 {
		t.Skipf("require one selected FT4232H; found %d", len(matches))
	}
	device, err := bus.Open(ctx, matches[0])
	if err != nil {
		t.Fatal(err)
	}
	channel, err := ftdi.Open(ctx, device, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 100_000})
	var owner interface{ Close() error } = device
	if channel != nil {
		owner = channel
	}
	released := true
	defer func() {
		if !released {
			t.Error("retaining wire after failed chain cleanup")
			return
		}
		for range 3 {
			if err = owner.Close(); err == nil {
				return
			}
		}
		t.Errorf("close: %v", err)
	}()
	if err != nil {
		t.Fatal(err)
	}
	exerciseJTAGChain(t, ctx, channel, &released)
}

func exerciseJTAGChain(t *testing.T, ctx context.Context, wire jtag.Wire, released *bool) {
	t.Helper()
	conn := jtag.New(wire)
	entries, err := conn.Discover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reset chain: %+v", entries)
	dap, _ := jtag.IDCODE(4, 0x5ba00477)
	ps, _ := jtag.IDCODE(12, 0x14730093)
	chain, err := jtag.NewChain(conn, jtag.Layout{dap, ps})
	if err != nil {
		t.Fatal(err)
	}
	*released = false
	defer func() {
		for range 3 {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = chain.Release(cleanup)
			cancel()
			if err == nil {
				*released = true
				return
			}
		}
		t.Errorf("release chain: %v", err)
	}()
	if err := chain.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tap, err := chain.TAP(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tap.ScanIR(ctx, []byte{0xe}); err != nil {
		t.Fatal(err)
	}
	data, err := tap.ScanDR(ctx, make([]byte, 4), 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 4 || data[0] != 0x77 || data[1] != 0x04 || data[2] != 0xa0 || data[3] != 0x5b {
		t.Fatalf("selected DAP IDCODE: %x", data)
	}
	t.Logf("selected DAP IDCODE: %x; explicit IR4/IR12 layout validated", data)
}
