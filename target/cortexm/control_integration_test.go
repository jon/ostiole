//go:build integration

package cortexm_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/discover"
	_ "github.com/jon/ostiole/discover/probes"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/target/cortexm"
)

func TestHILCortexM0Control(t *testing.T) {
	if os.Getenv("OSTIOLE_CORTEXM_HIL_CONTROL") != "1" {
		t.Skip("OSTIOLE_CORTEXM_HIL_CONTROL is not 1")
	}
	program := os.Getenv("OSTIOLE_CORTEXM_HIL_PROGRAM")
	counter, err := strconv.ParseUint(os.Getenv("OSTIOLE_CORTEXM_HIL_COUNTER"), 0, 32)
	if err != nil || counter%4 != 0 || program == "" {
		t.Fatal("require a known bench PROGRAM and aligned RAM COUNTER address")
	}
	for range 2 {
		if !t.Run("session", func(t *testing.T) { controlHIL(t, uint32(counter), program) }) {
			return
		}
	}
}

func controlHIL(t *testing.T, counter uint32, program string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	c := openControlBench(t, ctx)
	var core *cortexm.Target
	t.Cleanup(func() { releaseControlBench(t, core, c) })
	memory, err := c.OpenMemAP(ctx, dap.NewAPSel(0))
	if err != nil {
		t.Fatal(err)
	}
	before, err := memory.ReadWord(ctx, dhcsr)
	if err != nil {
		t.Fatal(err)
	}
	if before&haltStatus != 0 {
		t.Fatal("bench is already halted; refusing to resume it")
	}
	checkCounterHIL(t, ctx, memory, counter, "before acquisition", false)
	core, err = cortexm.Acquire(ctx, memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	checkCounterHIL(t, ctx, memory, counter, "halted", true)
	if err := core.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	checkCounterHIL(t, ctx, memory, counter, "resumed", false)
	if err := core.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	if err := core.Release(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := memory.ReadWord(ctx, dhcsr)
	if err != nil {
		t.Fatal(err)
	}
	mask := debugEnable | haltStatus
	if after&mask != before&mask {
		t.Fatalf("DHCSR before=%#x after=%#x", before, after)
	}
	checkCounterHIL(t, ctx, memory, counter, "released", false)
	t.Logf("micro:bit CMSIS-DAP 1 MHz AP0 CPUID=%#x program=%q counter=%#x DHCSR before=%#x after=%#x",
		core.Identity().Raw, program, counter, before, after)
}

func checkCounterHIL(t *testing.T, ctx context.Context, memory *dap.MemAP, addr uint32, phase string, stopped bool) {
	t.Helper()
	first, err := memory.ReadWord(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	last := first
	for range 10 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
		value, err := memory.ReadWord(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		changed = changed || value != first
		last = value
	}
	t.Logf("%s: counter[%#x] first=%#x last=%#x changed=%v", phase, addr, first, last, changed)
	if changed == stopped {
		t.Fatalf("counter at %#x: changed=%v stopped=%v", addr, changed, stopped)
	}
}

func openControlBench(t *testing.T, ctx context.Context) *armdebug.Conn {
	t.Helper()
	inventory, err := discover.Probes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := inventory.Select(discover.Selection{
		Provider: "cmsisdap", Serial: "9900360140124e4500279015000000360000000097969901",
	})
	if errors.Is(err, discover.ErrCandidateNotFound) || errors.Is(err, discover.ErrCandidateAmbiguous) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	c, err := armdebug.Open(ctx, discover.Selection{Binding: candidate.Info().Binding}, armdebug.Config{
		Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 1_000_000}),
	})
	if err != nil {
		if c != nil {
			releaseControlBench(t, nil, c)
		}
		t.Fatal(err)
	}
	return c
}

func releaseControlBench(t *testing.T, core *cortexm.Target, c *armdebug.Conn) {
	t.Helper()
	var err error
	for range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = core.Release(ctx)
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Errorf("target cleanup remains pending; retaining memory owner: %v", err)
		return
	}
	for range 3 {
		if err = c.Close(); err == nil {
			t.Log("target released and Arm debug owner closed")
			return
		}
	}
	t.Errorf("Arm debug cleanup remains pending: %v", err)
}
