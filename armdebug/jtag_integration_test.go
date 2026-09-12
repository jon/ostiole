//go:build integration

package armdebug_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/discover"
	ftdidiscovery "github.com/jon/ostiole/ftdi/discovery"
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
)

func TestHILArmJTAGOwnership(t *testing.T) {
	if os.Getenv("OSTIOLE_ARMDEBUG_JTAG_HIL") != "1" {
		t.Skip("set OSTIOLE_ARMDEBUG_JTAG_HIL=1 for FTDI 01691/A and the enabled ZCU104 chain")
	}
	var previous *armJTAGSnapshot
	for _, combined := range []bool{false, true} {
		for session := range 2 {
			if !t.Run(map[bool]string{false: "Connect", true: "Open"}[combined], func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				defer cancel()
				current := exerciseManagedJTAG(t, ctx, combined)
				if previous != nil && current != *previous {
					t.Fatalf("fresh session %d did not retain restored state: %+v != %+v", session, current, *previous)
				}
				previous = &current
			}) {
				return
			}
		}
	}
}

type armJTAGSnapshot struct {
	csw, tar, connectedControl uint32
}

func exerciseManagedJTAG(t *testing.T, ctx context.Context, combined bool) armJTAGSnapshot {
	t.Helper()
	inventory, err := discover.Probes(ctx)
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
	arm, _ := jtag.IDCODE(4, 0x5ba00477)
	xilinx, _ := jtag.IDCODE(12, 0x14730093)
	cfg := armdebug.Config{Port: armdebug.JTAGDP(probe.JTAGConfig{MaxClockHz: 100_000}, jtag.Layout{arm, xilinx}, 0), DAPOptions: []dap.Option{dap.WithMaxWaits(100)}}
	var c *armdebug.Conn
	if combined {
		c, err = armdebug.Open(ctx, discover.Selection{Binding: candidate.Info().Binding}, cfg)
	} else {
		var opened *probe.Probe
		opened, err = candidate.Open(ctx)
		if err != nil {
			if opened != nil {
				var cleanup error
				for range 3 {
					if cleanup = opened.Close(); cleanup == nil {
						break
					}
				}
				err = errors.Join(err, cleanup)
			}
			t.Fatal(err)
		}
		c, err = armdebug.Connect(ctx, opened, cfg)
	}
	if c != nil {
		t.Cleanup(func() { closeHIL(t, c) })
	}
	if err != nil {
		t.Fatal(err)
	}
	dp := c.Port()
	identity, _ := dp.Identity()
	id, present := identity.IDCODE()
	if !present || id != 0x5ba00477 {
		t.Fatalf("IDCODE: %#x", id)
	}
	state := snapshotManagedJTAG(t, ctx, dp)
	mem, err := c.OpenMemAP(ctx, dap.NewAPSel(1))
	if err != nil {
		t.Fatal(err)
	}
	var words [4]uint32
	for i := range words {
		words[i], err = mem.ReadWord(ctx, 0x80410ff0+uint32(i)*4)
		if err != nil {
			t.Fatal(err)
		}
	}
	if words != [4]uint32{0x0d, 0x90, 0x05, 0xb1} {
		t.Fatalf("component identity: %08x", words)
	}
	t.Logf("FTDI 01691/A 100 kHz IDCODE=%#08x AP1 CID=%08x saved/control=%+v", id, words, state)
	return state
}

func snapshotManagedJTAG(t *testing.T, ctx context.Context, dp *dap.DebugPort) armJTAGSnapshot {
	t.Helper()
	var state armJTAGSnapshot
	var err error
	state.connectedControl, err = dp.ReadDP(ctx, dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	sel := dap.NewAPSel(1)
	idr, err := dp.ReadAPIDR(ctx, sel)
	if err != nil || idr.Raw != 0x44770002 {
		t.Fatalf("AP1 identity: %#x %v", idr.Raw, err)
	}
	state.csw, err = dp.ReadRawAP(ctx, sel.Address(0))
	if err != nil {
		t.Fatal(err)
	}
	state.tar, err = dp.ReadRawAP(ctx, sel.Address(4))
	if err != nil {
		t.Fatal(err)
	}
	return state
}
