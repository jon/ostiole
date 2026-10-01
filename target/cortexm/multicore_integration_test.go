//go:build integration

package cortexm_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/target/cortexm"
)

const dualCounterProgram = "bf878b47815bc5eaf6afb5279efaa6ff163832bd178c5b7b1604f80e6ad6cde9"

func TestHILRP2350IndependentCores(t *testing.T) {
	if os.Getenv("OSTIOLE_RP2350_HIL_DUAL_CORE") != "1" {
		t.Skip("require OSTIOLE_RP2350_HIL_DUAL_CORE=1")
	}
	if os.Getenv("OSTIOLE_RP2350_HIL_PROGRAM") != dualCounterProgram {
		t.Fatal("require the documented two-core counter binary identity")
	}
	for range 2 {
		if !t.Run("session", dualCoreHIL) {
			return
		}
	}
}

type dualCoreBench struct {
	memory *dap.MemAP
	core   *cortexm.Target
	before uint32
	domain uint32
	cti    []uint32
}

func dualCoreHIL(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	bench := controlBench{name: "RP2350 independent cores", provider: "jlink", serial: "000802011345"}
	c := bench.open(t, ctx)
	var cores [2]dualCoreBench
	t.Cleanup(func() { releaseDualCoreBench(t, &cores, c) })
	for i, base := range []uint64{0x2000, 0x4000} {
		ap, err := dap.APAt(base)
		if err != nil {
			t.Fatal(err)
		}
		cores[i].memory, err = c.OpenMemAP(ctx, ap)
		if err != nil {
			t.Fatal(err)
		}
		checkDualCoreBench(t, ctx, &cores[i], i)
	}
	checkDualCounters(t, ctx, &cores, "before acquisition", [2]bool{})
	for i := range cores {
		var err error
		cores[i].core, err = cortexm.Acquire(ctx, cores[i].memory)
		if err != nil {
			t.Fatal(err)
		}
	}
	exerciseDualCores(t, ctx, &cores)
	for i := range cores {
		checkDualCoreRestored(t, ctx, &cores[i], i)
	}
}

func checkDualCoreBench(t *testing.T, ctx context.Context, b *dualCoreBench, index int) {
	t.Helper()
	identity, err := cortexm.Identify(ctx, b.memory)
	if err != nil || identity.Raw != 0x411fd210 {
		t.Fatalf("core %d CPUID=%#x: %v", index, identity.Raw, err)
	}
	b.cti = readInactiveCTI(t, ctx, b.memory)
	for i, want := range []uint32{0x4902b672, 0x30012000, 0xe7fc6008, 0x20040000 + uint32(index)*4} {
		addr := uint32(0x20040020 + index*0x40 + i*4)
		got, err := b.memory.ReadWord(ctx, addr)
		if err != nil || got != want {
			t.Fatalf("core %d counter code %#x=%#x, want %#x: %v", index, addr, got, want, err)
		}
	}
	b.before, err = b.memory.ReadWord(ctx, dhcsr)
	if err != nil || b.before&haltStatus != 0 {
		t.Fatalf("core %d must be running: DHCSR=%#x: %v", index, b.before, err)
	}
	b.domain, err = b.memory.ReadWord(ctx, 0xe000ee08)
	if err != nil || b.domain&(1<<16) == 0 {
		t.Fatalf("core %d requires Secure counter state: DSCSR=%#x: %v", index, b.domain, err)
	}
}

func readInactiveCTI(t *testing.T, ctx context.Context, memory *dap.MemAP) []uint32 {
	t.Helper()
	var values []uint32
	for _, item := range []struct{ offset, want uint32 }{
		{0xfbc, 0x47701a14}, {0xfc8, 0x00040800},
		{0, 0}, {0x14, 0}, {0x134, 0}, {0x138, 0}, {0xf00, 0},
	} {
		got, err := memory.ReadWord(ctx, 0xe0042000+item.offset)
		if err != nil || got != item.want {
			t.Fatalf("require inactive RP2350 CTI: offset=%#x value=%#x want=%#x: %v", item.offset, got, item.want, err)
		}
		values = append(values, got)
	}
	gate, err := memory.ReadWord(ctx, 0xe0042140)
	if err != nil {
		t.Fatal(err)
	}
	values = append(values, gate)
	for i := uint32(0); i < 8; i++ {
		for _, offset := range []uint32{0x20, 0xa0} {
			got, err := memory.ReadWord(ctx, 0xe0042000+offset+i*4)
			if err != nil || got != 0 {
				t.Fatalf("require unrouted CTI: offset=%#x value=%#x: %v", offset+i*4, got, err)
			}
			values = append(values, got)
		}
	}
	return values
}

func exerciseDualCores(t *testing.T, ctx context.Context, cores *[2]dualCoreBench) {
	t.Helper()
	for i := range cores {
		if err := cores[i].core.Halt(ctx); err != nil {
			t.Fatal(err)
		}
		stopped := [2]bool{}
		stopped[i] = true
		checkDualCounters(t, ctx, cores, "one core halted", stopped)
		checkDualCoreRegisters(t, ctx, &cores[i], i)
		checkCounterStepAtHIL(t, ctx, cores[i].core, cores[i].memory, i, uint32(0x20040026+i*0x40), uint32(0x20040000+i*4))
		checkDualCounters(t, ctx, cores, "one core stepped", stopped)
		if err := cores[i].core.Resume(ctx); err != nil {
			t.Fatal(err)
		}
		checkDualCounters(t, ctx, cores, "both resumed", [2]bool{})
	}
	for i := range cores {
		if err := cores[i].core.Halt(ctx); err != nil {
			t.Fatal(err)
		}
	}
	checkDualCounters(t, ctx, cores, "both halted sequentially", [2]bool{true, true})
	if err := cores[0].core.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	checkDualCounters(t, ctx, cores, "only core 0 resumed", [2]bool{false, true})
	if err := cores[1].core.Release(ctx); err != nil {
		t.Fatal(err)
	}
	checkDualCounters(t, ctx, cores, "core 1 released from halt", [2]bool{})
	if err := cores[0].core.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cores[0].core.Release(ctx); err != nil {
		t.Fatal(err)
	}
	checkDualCounters(t, ctx, cores, "both released", [2]bool{})
}

func checkDualCoreRegisters(t *testing.T, ctx context.Context, b *dualCoreBench, index int) {
	t.Helper()
	saved := readRegistersHIL(t, ctx, b.core)
	pc := saved[cortexm.PC]
	start := uint32(0x20040026 + index*0x40)
	if (pc != start && pc != start+2 && pc != start+4) || saved[cortexm.R1] != uint32(0x20040000+index*4) || saved[cortexm.XPSR]&0x010001ff != 0x01000000 {
		t.Fatalf("core %d unexpected counter registers: PC=%#x R1=%#x XPSR=%#x", index, pc, saved[cortexm.R1], saved[cortexm.XPSR])
	}
	t.Logf("core %d: all 19 registers read, PC=%#x R1=%#x", index, pc, saved[cortexm.R1])
}

func checkDualCounters(t *testing.T, ctx context.Context, cores *[2]dualCoreBench, phase string, stopped [2]bool) {
	t.Helper()
	var first, last [2]uint32
	var changed [2]bool
	for n := range 11 {
		if n != 0 {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		for i := range cores {
			value, err := cores[i].memory.ReadWord(ctx, uint32(0x20040000+i*4))
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				first[i] = value
			}
			last[i] = value
			changed[i] = changed[i] || value != first[i]
		}
	}
	t.Logf("%s: first=%#x last=%#x changed=%v stopped=%v", phase, first, last, changed, stopped)
	for i := range cores {
		if changed[i] == stopped[i] {
			t.Fatalf("core %d changed=%v stopped=%v", i, changed[i], stopped[i])
		}
	}
}

func checkDualCoreRestored(t *testing.T, ctx context.Context, b *dualCoreBench, index int) {
	t.Helper()
	after, err := b.memory.ReadWord(ctx, dhcsr)
	mask := debugEnable | haltStatus
	if b.before&debugEnable != 0 {
		mask |= 12
	}
	if err != nil || after&mask != b.before&mask {
		t.Fatalf("core %d DHCSR before=%#x after=%#x: %v", index, b.before, after, err)
	}
	domain, err := b.memory.ReadWord(ctx, 0xe000ee08)
	if err != nil || domain != b.domain {
		t.Fatalf("core %d DSCSR before=%#x after=%#x: %v", index, b.domain, domain, err)
	}
	for i, value := range readInactiveCTI(t, ctx, b.memory) {
		if value != b.cti[i] {
			t.Fatalf("core %d CTI snapshot changed", index)
		}
	}
	t.Logf("core %d AP=%#x CPUID=0x411fd210 DHCSR before=%#x after=%#x DSCSR=%#x CTI unchanged (gate=%#x)", index, 0x2000+index*0x2000, b.before, after, domain, b.cti[7])
}

func releaseDualCoreBench(t *testing.T, cores *[2]dualCoreBench, c *armdebug.Conn) {
	t.Helper()
	pending := false
	for i := len(cores) - 1; i >= 0; i-- {
		var err error
		for range 3 {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = cores[i].core.Release(ctx)
			cancel()
			if err == nil {
				break
			}
		}
		if err != nil {
			t.Errorf("core %d cleanup pending; retaining Arm owner: %v", i, err)
			pending = true
		}
	}
	if pending {
		return
	}
	var err error
	for range 3 {
		if err = c.Close(); err == nil {
			t.Log("both targets released and shared Arm debug owner closed")
			return
		}
	}
	t.Errorf("shared Arm debug owner cleanup remains pending: %v", err)
}
