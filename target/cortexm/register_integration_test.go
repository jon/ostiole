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

func TestHILCortexM0Registers(t *testing.T) {
	if os.Getenv("OSTIOLE_CORTEXM_HIL_CONTROL") != "1" || os.Getenv("OSTIOLE_CORTEXM_HIL_REGISTERS") != "1" {
		t.Skip("require OSTIOLE_CORTEXM_HIL_CONTROL=1 and OSTIOLE_CORTEXM_HIL_REGISTERS=1")
	}
	if os.Getenv("OSTIOLE_CORTEXM_HIL_PROGRAM") != "sha256:ee294cc06ab6e8228161b49506675b065c0148b26421cf1f83c8e45e35cd4e5d" {
		t.Fatal("require the documented counter image identity in OSTIOLE_CORTEXM_HIL_PROGRAM")
	}
	for range 2 {
		if !t.Run("session", registerHIL) {
			return
		}
	}
}

func registerHIL(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	c := openControlBench(t, ctx)
	var core *cortexm.Target
	dirty := false
	t.Cleanup(func() {
		if dirty {
			t.Error("register restoration unconfirmed; retaining both owners without requesting resume")
			return
		}
		releaseControlBench(t, core, c)
	})
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
	saved := readRegistersHIL(t, ctx, core)
	checkCounterRegisters(t, saved)
	exerciseRegistersHIL(t, ctx, core, saved, &dirty)
	for reg, want := range saved {
		if got := readRegisterHIL(t, ctx, core, reg); got != want {
			t.Fatalf("register %d changed: %#x, want %#x", reg, got, want)
		}
	}
	dirty = false
	checkCounterHIL(t, ctx, memory, 0x20000000, "after restored registers, still halted", true)
	if err := core.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	checkCounterHIL(t, ctx, memory, 0x20000000, "resumed", false)
	if err := core.Release(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := memory.ReadWord(ctx, dhcsr)
	if err != nil {
		t.Fatal(err)
	}
	if after&(debugEnable|haltStatus) != before&(debugEnable|haltStatus) {
		t.Fatalf("DHCSR before=%#x after=%#x", before, after)
	}
	checkCounterHIL(t, ctx, memory, 0x20000000, "released", false)
	t.Logf("micro:bit CMSIS-DAP 1 MHz AP0 CPUID=%#x DHCSR before=%#x after=%#x", core.Identity().Raw, before, after)
}

func checkRegisterFirmware(t *testing.T, ctx context.Context, memory *dap.MemAP) {
	t.Helper()
	// Match the linked vectors and instructions before changing processor state.
	for addr := uint32(0); addr < 0xc0; addr += 4 {
		want := uint32(0xcd)
		switch addr {
		case 0:
			want = 0x20004000
		case 4:
			want = 0xc1
		}
		got, err := memory.ReadWord(ctx, addr)
		if err != nil || got != want {
			t.Fatalf("counter vector %#x=%#x, want %#x: %v", addr, got, want, err)
		}
	}
	for i, want := range []uint32{0x4903b672, 0x30012000, 0xe7fc6008, 0x0000e7fe, 0x20000000} {
		addr := uint32(0xc0 + i*4)
		got, err := memory.ReadWord(ctx, addr)
		if err != nil || got != want {
			t.Fatalf("counter code %#x=%#x, want %#x: %v", addr, got, want, err)
		}
	}
}

type registerReaderHIL interface {
	ReadRegister(context.Context, cortexm.Register) (uint32, error)
}

func readRegisterHIL(t *testing.T, ctx context.Context, core registerReaderHIL, reg cortexm.Register) uint32 {
	t.Helper()
	value, err := core.ReadRegister(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func readRegistersHIL(t *testing.T, ctx context.Context, core registerReaderHIL) map[cortexm.Register]uint32 {
	t.Helper()
	saved := make(map[cortexm.Register]uint32)
	for _, reg := range []cortexm.Register{
		cortexm.R0, cortexm.R1, cortexm.R2, cortexm.R3, cortexm.R4, cortexm.R5,
		cortexm.R6, cortexm.R7, cortexm.R8, cortexm.R9, cortexm.R10, cortexm.R11,
		cortexm.R12, cortexm.SP, cortexm.LR, cortexm.PC, cortexm.XPSR, cortexm.MSP, cortexm.PSP,
	} {
		saved[reg] = readRegisterHIL(t, ctx, core, reg)
		t.Logf("register %d=%#08x", reg, saved[reg])
	}
	return saved
}

func exerciseRegistersHIL(t *testing.T, ctx context.Context, core *cortexm.Target, saved map[cortexm.Register]uint32, dirty *bool) {
	t.Helper()
	for _, tc := range []struct {
		reg   cortexm.Register
		value uint32
	}{
		{cortexm.R4, 0x55aa55aa}, {cortexm.R4, 0xaa55aa55},
		{cortexm.SP, 0x20003ff0}, {cortexm.MSP, 0x20003fe0},
		{cortexm.PSP, 0x20003fd0}, {cortexm.PC, saved[cortexm.PC] ^ 2},
	} {
		*dirty = true
		if err := core.WriteRegister(ctx, tc.reg, tc.value); err != nil {
			t.Fatal(err)
		}
		if got := readRegisterHIL(t, ctx, core, tc.reg); got != tc.value {
			t.Fatalf("write register %d: %#x, want %#x", tc.reg, got, tc.value)
		}
		if tc.reg == cortexm.SP || tc.reg == cortexm.MSP {
			if got := readRegisterHIL(t, ctx, core, cortexm.SP); got != tc.value {
				t.Fatal("SP does not alias MSP")
			}
			if got := readRegisterHIL(t, ctx, core, cortexm.MSP); got != tc.value {
				t.Fatal("MSP does not alias SP")
			}
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := core.WriteRegister(cleanup, tc.reg, saved[tc.reg])
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if got := readRegisterHIL(t, ctx, core, tc.reg); got != saved[tc.reg] {
			t.Fatalf("register %d restoration: %#x, want %#x", tc.reg, got, saved[tc.reg])
		}
		t.Logf("register %d wrote %#08x and restored %#08x", tc.reg, tc.value, saved[tc.reg])
	}
}

func checkCounterRegisters(t *testing.T, saved map[cortexm.Register]uint32) {
	t.Helper()
	if pc := saved[cortexm.PC]; pc != 0xc6 && pc != 0xc8 && pc != 0xca {
		t.Fatalf("PC outside counter loop: %#x", pc)
	}
	if saved[cortexm.SP] != 0x20004000 || saved[cortexm.MSP] != 0x20004000 || saved[cortexm.XPSR]&0x010001ff != 0x01000000 {
		t.Fatal("unexpected counter stack or execution state")
	}
}
