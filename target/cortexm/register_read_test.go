package cortexm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

func acquireRegisters(t *testing.T, m *registerMemory, inherited bool) *cortexm.Target {
	t.Helper()
	if inherited {
		m.control, m.halted = debugEnable, true
	}
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !inherited {
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	return core
}

func TestReadRegisters(t *testing.T) {
	for _, delay := range []int{0, 2} {
		for _, inherited := range []bool{false, true} {
			m := newRegisterMemory()
			m.delay = delay
			core := acquireRegisters(t, m, inherited)
			for reg := cortexm.R0; reg <= cortexm.PSP; reg++ {
				value, err := core.ReadRegister(t.Context(), reg)
				if err != nil || value != m.registers[int(reg)-1] {
					t.Fatalf("reg=%d value=%#x err=%v", reg, value, err)
				}
			}
			if err := core.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if m.halted != inherited {
				t.Fatal("halt ownership changed")
			}
		}
	}
}

func TestRegisterReadRejectsBeforeTransfer(t *testing.T) {
	m := newRegisterMemory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	for _, reg := range []cortexm.Register{0, 20, 255} {
		reads, writes := m.reads, m.writes
		if _, err := core.ReadRegister(t.Context(), reg); err == nil || m.reads != reads || m.writes != writes {
			t.Fatal("invalid register reached memory")
		}
	}
	writes := m.writes
	if _, err := core.ReadRegister(t.Context(), cortexm.R0); err == nil || m.writes != writes {
		t.Fatal("running read started transfer")
	}
	if err := core.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reads := m.reads
	if _, err := core.ReadRegister(ctx, cortexm.R0); !errors.Is(err, context.Canceled) || m.reads != reads {
		t.Fatal("canceled read reached memory")
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := core.ReadRegister(t.Context(), cortexm.R0); err == nil {
		t.Fatal("released target read")
	}
	var zero cortexm.Target
	if _, err := zero.ReadRegister(t.Context(), cortexm.R0); err == nil {
		t.Fatal("zero target read")
	}
}

func TestRegisterReadPendingCleanup(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		m := newRegisterMemory()
		core := acquireRegisters(t, m, inherited)
		m.block = true
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
		_, err := core.ReadRegister(ctx, cortexm.R0)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		writes := m.writes
		ctx, cancel = context.WithTimeout(t.Context(), 5*time.Millisecond)
		err = core.Release(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || m.writes != writes || !m.halted {
			t.Fatal("pending transfer released")
		}
		if _, err := core.ReadRegister(t.Context(), cortexm.R0); err == nil {
			t.Fatal("ordinary call during cleanup")
		}
		m.block = false
		if err := core.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if m.transfers != 1 || m.halted != inherited {
			t.Fatal("transfer replay or halt ownership changed")
		}
	}
}

func TestRegisterReadObservesLostControl(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		m := newRegisterMemory()
		core := acquireRegisters(t, m, false)
		m.control, m.halted = debugEnable, false
		if disabled {
			m.control = 0
		}
		if _, err := core.ReadRegister(t.Context(), cortexm.R4); err == nil {
			t.Fatal("lost control accepted")
		}
		m.control, m.halted = debugEnable|haltRequest, true
		writes := m.writes
		if err := core.Resume(t.Context()); err == nil || m.writes != writes {
			t.Fatal("resumed independent stop")
		}
		if disabled {
			if _, err := core.ReadRegister(t.Context(), cortexm.R4); err == nil {
				t.Fatal("ordinary call after disabled debug")
			}
		}
	}
}
