//go:build integration

package coresight_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/coresight"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/discover"
	_ "github.com/jon/ostiole/discover/probes"
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
)

func TestHILComponentIdentity(t *testing.T) {
	if os.Getenv("OSTIOLE_CORESIGHT_HIL") != "1" {
		t.Skip("set OSTIOLE_CORESIGHT_HIL=1 for the micro:bit SWD and externally enabled ZCU104 JTAG benches")
	}
	arm, _ := jtag.IDCODE(4, 0x5ba00477)
	xilinx, _ := jtag.IDCODE(12, 0x14730093)
	for _, bench := range []struct {
		name      string
		selection discover.Selection
		port      armdebug.PortConfig
		ap        uint8
		base      uint64
		class     uint8
	}{
		{"microbit", discover.Selection{Provider: "cmsisdap", Serial: "9900360140124e4500279015000000360000000097969901"}, armdebug.SWDP(probe.SWDConfig{MaxClockHz: 100_000}), 0, 0xe00ff000, 1},
		{"zcu104", discover.Selection{Provider: "ftdi", Serial: "01691", Function: "A"}, armdebug.JTAGDP(probe.JTAGConfig{MaxClockHz: 100_000}, jtag.Layout{arm, xilinx}, 0), 1, 0x80410000, 9},
	} {
		t.Run(bench.name, func(t *testing.T) {
			for session := range 2 {
				if !t.Run("session", func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
					defer cancel()
					c := openIdentityBench(t, ctx, bench.selection, bench.port)
					memory, err := c.OpenMemAP(ctx, dap.NewAPSel(bench.ap))
					if err != nil {
						t.Fatal(err)
					}
					inspectAdvertisedRoot(t, ctx, memory)
					got, err := coresight.Identify(ctx, memory, bench.base)
					if err != nil {
						t.Fatal(err)
					}
					if got.Class() != bench.class {
						t.Fatalf("class=%#x, want %#x", got.Class(), bench.class)
					}
					t.Logf("session=%d AP%d 100 kHz identity=%+v", session, bench.ap, got)
				}) {
					return
				}
			}
		})
	}
}

func openIdentityBench(t *testing.T, ctx context.Context, selection discover.Selection, port armdebug.PortConfig) *armdebug.Conn {
	t.Helper()
	inventory, err := discover.Probes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := inventory.Select(selection)
	if errors.Is(err, discover.ErrCandidateNotFound) || errors.Is(err, discover.ErrCandidateAmbiguous) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	c, err := armdebug.Open(ctx, discover.Selection{Binding: candidate.Info().Binding}, armdebug.Config{Port: port, DAPOptions: []dap.Option{dap.WithMaxWaits(100)}})
	if c != nil {
		t.Cleanup(func() {
			var cleanup error
			for range 3 {
				if cleanup = c.Close(); cleanup == nil {
					t.Log("Arm debug owner closed")
					return
				}
			}
			t.Errorf("cleanup remains pending after three attempts: %v", cleanup)
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func inspectAdvertisedRoot(t *testing.T, ctx context.Context, memory *dap.MemAP) {
	t.Helper()
	base, present, err := memory.ReadDebugBase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("MEM-AP advertises no debug entry")
	}
	root, err := coresight.Identify(ctx, memory, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("advertised base=%#x CIDR=%#x PIDR=%#x", base, root.CIDR, root.PIDR)
}
