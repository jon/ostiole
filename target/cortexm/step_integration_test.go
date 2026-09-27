//go:build integration

package cortexm_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/target/cortexm"
)

func TestHILCortexM0Step(t *testing.T) {
	if os.Getenv("OSTIOLE_CORTEXM_HIL_CONTROL") != "1" || os.Getenv("OSTIOLE_CORTEXM_HIL_STEP") != "1" {
		t.Skip("require OSTIOLE_CORTEXM_HIL_CONTROL=1 and OSTIOLE_CORTEXM_HIL_STEP=1")
	}
	if os.Getenv("OSTIOLE_CORTEXM_HIL_PROGRAM") != "sha256:ee294cc06ab6e8228161b49506675b065c0148b26421cf1f83c8e45e35cd4e5d" {
		t.Fatal("require the documented counter image identity in OSTIOLE_CORTEXM_HIL_PROGRAM")
	}
	for range 2 {
		if !t.Run("session", stepHIL) {
			return
		}
	}
}

func stepHIL(t *testing.T) {
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
	checkRegisterFirmware(t, ctx, memory)
	before, err := memory.ReadWord(ctx, dhcsr)
	if err != nil {
		t.Fatal(err)
	}
	if before&haltStatus != 0 {
		t.Fatal("bench is already halted; refusing to resume it")
	}
	checkCounterHIL(t, ctx, memory, 0x20000000, "before acquisition", false)
	core, err = cortexm.Acquire(ctx, memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	if r1 := readRegisterHIL(t, ctx, core, cortexm.R1); r1 != 0x20000000 {
		t.Fatalf("unexpected counter address in R1: %#x", r1)
	}
	for n := range 12 {
		checkCounterStepHIL(t, ctx, core, memory, n)
	}
	checkCounterHIL(t, ctx, memory, 0x20000000, "after steps, still halted", true)
	if err := core.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	checkCounterHIL(t, ctx, memory, 0x20000000, "resumed", false)
	if err := core.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	checkCounterStepHIL(t, ctx, core, memory, 12)
	if err := core.Release(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := memory.ReadWord(ctx, dhcsr)
	if err != nil {
		t.Fatal(err)
	}
	mask := debugEnable | haltStatus
	if before&debugEnable != 0 {
		mask |= 12
	}
	if after&mask != before&mask {
		t.Fatalf("DHCSR before=%#x after=%#x", before, after)
	}
	checkCounterHIL(t, ctx, memory, 0x20000000, "released after another step", false)
	t.Logf("micro:bit CMSIS-DAP 1 MHz AP0 CPUID=%#x DHCSR before=%#x after=%#x", core.Identity().Raw, before, after)
}

func checkCounterStepHIL(t *testing.T, ctx context.Context, core *cortexm.Target, memory *dap.MemAP, n int) {
	t.Helper()
	pc := readRegisterHIL(t, ctx, core, cortexm.PC)
	r0 := readRegisterHIL(t, ctx, core, cortexm.R0)
	counter, err := memory.ReadWord(ctx, 0x20000000)
	if err != nil {
		t.Fatal(err)
	}
	nextPC, nextR0, nextCounter := pc, r0, counter
	switch pc {
	case 0xc6:
		nextPC = 0xc8
		nextR0++
	case 0xc8:
		nextPC = 0xca
		nextCounter = r0
	case 0xca:
		nextPC = 0xc6
	default:
		t.Fatalf("PC outside counter loop: %#x", pc)
	}
	if err := core.Step(ctx); err != nil {
		t.Fatal(err)
	}
	gotPC := readRegisterHIL(t, ctx, core, cortexm.PC)
	gotR0 := readRegisterHIL(t, ctx, core, cortexm.R0)
	gotCounter, err := memory.ReadWord(ctx, 0x20000000)
	if err != nil {
		t.Fatal(err)
	}
	halted, err := core.Halted(ctx)
	if err != nil || !halted {
		t.Fatalf("halted=%v err=%v", halted, err)
	}
	if gotPC != nextPC || gotR0 != nextR0 || gotCounter != nextCounter {
		t.Fatalf("step %d: PC %#x -> %#x (want %#x), R0 %#x -> %#x (want %#x), RAM %#x -> %#x (want %#x)", n, pc, gotPC, nextPC, r0, gotR0, nextR0, counter, gotCounter, nextCounter)
	}
	t.Logf("step %d: PC %#x -> %#x, R0 %#x -> %#x, RAM %#x -> %#x", n, pc, gotPC, r0, gotR0, counter, gotCounter)
}
