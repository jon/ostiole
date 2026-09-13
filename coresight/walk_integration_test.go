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
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
)

type romBench struct {
	name      string
	selection discover.Selection
	port      armdebug.PortConfig
	ap        uint8
	count     int
	fault     uint64
}

func TestHILROMWalk(t *testing.T) {
	if os.Getenv("OSTIOLE_ROM_HIL") != "1" {
		t.Skip("set OSTIOLE_ROM_HIL=1 for the micro:bit and externally enabled ZCU104 benches")
	}
	arm, _ := jtag.IDCODE(4, 0x5ba00477)
	xilinx, _ := jtag.IDCODE(12, 0x14730093)
	for _, bench := range []romBench{
		{"microbit", discover.Selection{Provider: "cmsisdap", Serial: "9900360140124e4500279015000000360000000097969901"}, armdebug.SWDP(probe.SWDConfig{MaxClockHz: 100_000}), 0, 6, 0},
		{"zcu104", discover.Selection{Provider: "ftdi", Serial: "01691", Function: "A"}, armdebug.JTAGDP(probe.JTAGConfig{MaxClockHz: 100_000}, jtag.Layout{arm, xilinx}, 0), 1, 18, 0x803e0000},
	} {
		t.Run(bench.name, func(t *testing.T) {
			for session := range 2 {
				if !t.Run("session", func(t *testing.T) { observeROMWalk(t, bench, session) }) {
					return
				}
			}
		})
	}
}

func observeROMWalk(t *testing.T, bench romBench, session int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	owner := openIdentityBench(t, ctx, bench.selection, bench.port)
	memory, err := owner.OpenMemAP(ctx, dap.NewAPSel(bench.ap))
	if err != nil {
		t.Fatal(err)
	}
	base, present, err := memory.ReadDebugBase(ctx)
	if err != nil || !present {
		t.Fatalf("BASE=%#x,%v,%v", base, present, err)
	}
	limits := coresight.WalkLimits{MaxDepth: 8, MaxComponents: 256, MaxEntries: 4096}
	visits, err := coresight.Walk(ctx, memory, base, limits)
	for i, v := range visits {
		if v.Component != nil {
			t.Logf("visit=%d parent=%d entry=%d base=%#x class=%#x part=%#x", i, v.Parent, v.Index, v.Component.Base, v.Component.Class(), v.Component.Part())
		}
		if v.Err != nil {
			t.Logf("visit=%d parent=%d entry=%d base=%#x: %v", i, v.Parent, v.Index, v.Entry.Base, v.Err)
		}
	}
	t.Logf("session=%d AP%d 100 kHz root=%#x visits=%d complete=%t error=%v", session, bench.ap, base, len(visits), err == nil, err)
	checkROMWalkObservation(t, bench, visits, err)
}

func checkROMWalkObservation(t *testing.T, bench romBench, visits []coresight.Visit, err error) {
	t.Helper()
	if len(visits) != bench.count {
		t.Fatalf("visits=%d, want %d", len(visits), bench.count)
	}
	if bench.fault == 0 {
		if err != nil {
			t.Fatal(err)
		}
	} else {
		last := visits[len(visits)-1]
		if !errors.Is(err, dap.ErrFault) || !errors.Is(last.Err, dap.ErrFault) || last.Entry.Base != bench.fault || last.Component != nil {
			t.Fatalf("expected inaccessible component %#x; last=%+v err=%v", bench.fault, last, err)
		}
		visits = visits[:len(visits)-1]
	}
	for i, v := range visits {
		if v.Component == nil || v.Err != nil {
			t.Fatalf("identity %d=%+v", i, v)
		}
	}
}
