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

func TestHILRP2350Step(t *testing.T) {
	if os.Getenv("OSTIOLE_RP2350_HIL_STEP") != "1" || os.Getenv("OSTIOLE_RP2350_HIL_CONTROL") != "1" {
		t.Skip("require OSTIOLE_RP2350_HIL_STEP=1 and OSTIOLE_RP2350_HIL_CONTROL=1")
	}
	if os.Getenv("OSTIOLE_RP2350_HIL_PROGRAM") != "c20737e61153b272322548e8e6db5c420f0c148d6707ca4412c309f70415065a" {
		t.Fatal("require the documented RP2350 counter binary identity")
	}
	for range 2 {
		if !t.Run("session", m33StepHIL) {
			return
		}
	}
}

func m33StepHIL(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ap, err := dap.APAt(0x2000)
	if err != nil {
		t.Fatal(err)
	}
	bench := controlBench{name: "RP2350 core 0", provider: "jlink", serial: "000802011345", ap: ap, cpuid: 0x411fd210}
	c := bench.open(t, ctx)
	var core *cortexm.Target
	t.Cleanup(func() { releaseControlBench(t, core, c) })
	memory, err := c.OpenMemAP(ctx, ap)
	if err != nil {
		t.Fatal(err)
	}
	before := checkM33RegisterBench(t, ctx, memory)
	checkCounterHIL(t, ctx, memory, 0x20040000, "before acquisition", false)
	core, err = cortexm.Acquire(ctx, memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	exerciseM33StepsHIL(t, ctx, core, memory)
	checkCounterHIL(t, ctx, memory, 0x20040000, "after steps, halted", true)
	if err := core.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	checkCounterHIL(t, ctx, memory, 0x20040000, "resumed", false)
	if err := core.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	checkCounterStepAtHIL(t, ctx, core, memory, 12, 0x20040026, 0x20040000)
	if err := core.Release(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := memory.ReadWord(ctx, dhcsr)
	mask := debugEnable | haltStatus
	if before&debugEnable != 0 {
		mask |= 12
	}
	if err != nil || after&mask != before&mask {
		t.Fatalf("DHCSR before=%#x after=%#x: %v", before, after, err)
	}
	checkCounterHIL(t, ctx, memory, 0x20040000, "released after another step", false)
	t.Logf("J-Link %s 1 MHz AP 0x2000 CPUID=%#x DHCSR before=%#x after=%#x", bench.serial, core.Identity().Raw, before, after)
}

func exerciseM33StepsHIL(t *testing.T, ctx context.Context, core *cortexm.Target, memory *dap.MemAP) {
	t.Helper()
	const dscsr = uint32(0xe000ee08)
	domain, err := memory.ReadWord(ctx, dscsr)
	if err != nil || domain&(1<<16) == 0 {
		t.Fatalf("require Secure counter state: DSCSR=%#x: %v", domain, err)
	}
	if r1 := readRegisterHIL(t, ctx, core, cortexm.R1); r1 != 0x20040000 {
		t.Fatalf("counter address in R1=%#x", r1)
	}
	if xpsr := readRegisterHIL(t, ctx, core, cortexm.XPSR); xpsr&0x010001ff != 0x01000000 {
		t.Fatalf("counter state XPSR=%#x", xpsr)
	}
	for n := range 12 {
		checkCounterStepAtHIL(t, ctx, core, memory, n, 0x20040026, 0x20040000)
	}
	after, err := memory.ReadWord(ctx, dscsr)
	if err != nil || after != domain {
		t.Fatalf("DSCSR before=%#x after=%#x: %v", domain, after, err)
	}
	t.Logf("DSCSR preserved at %#x", domain)
}
