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

func TestHILRP2350Registers(t *testing.T) {
	if os.Getenv("OSTIOLE_RP2350_HIL_REGISTERS") != "1" || os.Getenv("OSTIOLE_RP2350_HIL_CONTROL") != "1" {
		t.Skip("require OSTIOLE_RP2350_HIL_REGISTERS=1 and OSTIOLE_RP2350_HIL_CONTROL=1")
	}
	if os.Getenv("OSTIOLE_RP2350_HIL_PROGRAM") != "c20737e61153b272322548e8e6db5c420f0c148d6707ca4412c309f70415065a" {
		t.Fatal("require the documented RP2350 counter binary identity")
	}
	for range 2 {
		if !t.Run("session", m33RegisterHIL) {
			return
		}
	}
}

func m33RegisterHIL(t *testing.T) {
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
	dirty := false
	t.Cleanup(func() {
		if dirty {
			t.Error("register restoration unconfirmed; retaining owners without requesting resume")
			return
		}
		releaseControlBench(t, core, c)
	})
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
	exerciseM33RegistersHIL(t, ctx, core, memory, &dirty)
	checkCounterHIL(t, ctx, memory, 0x20040000, "registers restored, halted", true)
	if err := core.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	checkCounterHIL(t, ctx, memory, 0x20040000, "resumed", false)
	if err := core.Release(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := memory.ReadWord(ctx, dhcsr)
	if err != nil || after&(debugEnable|haltStatus) != before&(debugEnable|haltStatus) {
		t.Fatalf("DHCSR before=%#x after=%#x: %v", before, after, err)
	}
	checkCounterHIL(t, ctx, memory, 0x20040000, "released", false)
	t.Logf("J-Link %s 1 MHz AP 0x2000 CPUID=%#x DHCSR before=%#x after=%#x", bench.serial, core.Identity().Raw, before, after)
}

func checkM33RegisterBench(t *testing.T, ctx context.Context, memory *dap.MemAP) uint32 {
	t.Helper()
	identity, err := cortexm.Identify(ctx, memory)
	if err != nil || identity.Raw != 0x411fd210 {
		t.Fatalf("CPUID=%#x: %v", identity.Raw, err)
	}
	for i, want := range []uint32{0x4902b672, 0x30012000, 0xe7fc6008, 0x20040000} {
		addr := uint32(0x20040020 + i*4)
		got, err := memory.ReadWord(ctx, addr)
		if err != nil || got != want {
			t.Fatalf("counter code %#x=%#x, want %#x: %v", addr, got, want, err)
		}
	}
	before, err := memory.ReadWord(ctx, dhcsr)
	if err != nil || before&haltStatus != 0 {
		t.Fatalf("bench must be running: DHCSR=%#x: %v", before, err)
	}
	return before
}

func exerciseM33RegistersHIL(t *testing.T, ctx context.Context, core *cortexm.Target, memory *dap.MemAP, dirty *bool) {
	t.Helper()
	const dscsr = uint32(0xe000ee08)
	domain, err := memory.ReadWord(ctx, dscsr)
	if err != nil || domain&(1<<16) == 0 {
		t.Fatalf("require Secure counter state: DSCSR=%#x: %v", domain, err)
	}
	saved := readRegistersHIL(t, ctx, core)
	pc := saved[cortexm.PC]
	if (pc != 0x20040026 && pc != 0x20040028 && pc != 0x2004002a) || saved[cortexm.XPSR]&0x010001ff != 0x01000000 {
		t.Fatal("unexpected counter execution state")
	}
	if saved[cortexm.SP] != saved[cortexm.MSP] {
		t.Fatal("counter must use main stack")
	}
	exerciseRegistersHIL(t, ctx, core, saved, dirty)
	for reg, want := range saved {
		if got := readRegisterHIL(t, ctx, core, reg); got != want {
			t.Fatalf("register %d=%#x, want %#x", reg, got, want)
		}
	}
	after, err := memory.ReadWord(ctx, dscsr)
	if err != nil || after != domain {
		t.Fatalf("DSCSR before=%#x after=%#x: %v", domain, after, err)
	}
	*dirty = false
	t.Logf("DSCSR preserved at %#x; all 19 registers restored", domain)
}
