package cortexm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

func acquireStep(t *testing.T, m *stepMemory) *cortexm.Target {
	t.Helper()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	return core
}

func TestStepReturnsHalted(t *testing.T) {
	for _, delay := range []int{0, 2} {
		t.Run("step", func(t *testing.T) {
			m := newStepMemory()
			m.stepDelay = delay
			core := acquireStep(t, m)
			firstPC := m.registers[15]
			for n := 1; n <= 3; n++ {
				if err := core.Step(t.Context()); err != nil {
					t.Fatal(err)
				}
				pc, err := core.ReadRegister(t.Context(), cortexm.PC)
				if err != nil || pc != firstPC+uint32(n)*2 || !m.halted || m.control != debugEnable|haltRequest {
					t.Fatalf("PC=%#x err=%v control=%#x", pc, err, m.control)
				}
			}
			if m.steps != 3 || m.launches != 3 {
				t.Fatal("step replay")
			}
			if err := core.Resume(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := core.Release(t.Context()); err != nil || m.control != 0 || m.halted {
				t.Fatalf("release: %v", err)
			}
		})
	}
}

func TestStepRequiresOwnedHalt(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		m := newStepMemory()
		if inherited {
			m.control, m.halted = debugEnable|haltRequest, true
		}
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		writes := m.writes
		if err := core.Step(t.Context()); err == nil || m.writes != writes {
			t.Fatal("unowned step")
		}
		if err := core.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var core *cortexm.Target
	if err := core.Step(t.Context()); err == nil {
		t.Fatal("nil target step")
	}
	var zero cortexm.Target
	if err := zero.Step(t.Context()); err == nil {
		t.Fatal("zero target step")
	}
}

func TestStepCancellationBeforeLaunch(t *testing.T) {
	for _, atRead := range []bool{false, true} {
		m := newStepMemory()
		core := acquireStep(t, m)
		ctx, cancel := context.WithCancel(t.Context())
		if atRead {
			m.onRead = cancel
		} else {
			cancel()
		}
		writes := m.writes
		if err := core.Step(ctx); !errors.Is(err, context.Canceled) || m.writes != writes {
			t.Fatalf("step: %v", err)
		}
		m.onRead = nil
		if err := core.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := core.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStepPendingCleanup(t *testing.T) {
	m := newStepMemory()
	core := acquireStep(t, m)
	m.blockStep = true
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	err := core.Step(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	writes := m.writes
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Millisecond)
	err = core.Release(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || m.writes != writes {
		t.Fatal("cleanup changed control while step runs")
	}
	if err := core.Step(t.Context()); err == nil {
		t.Fatal("ordinary step during cleanup")
	}
	m.blockStep = false
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.launches != 1 || m.steps != 1 || m.control != 0 || m.halted {
		t.Fatal("cleanup replay or failed restoration")
	}
}

func TestStepObservesLostOwnership(t *testing.T) {
	m := newStepMemory()
	core := acquireStep(t, m)
	m.control, m.halted = debugEnable, false
	if err := core.Step(t.Context()); err == nil {
		t.Fatal("step without halt")
	}
	m.control, m.halted = debugEnable|haltRequest, true
	writes := m.writes
	if err := core.Step(t.Context()); err == nil || m.writes != writes {
		t.Fatal("step after lost ownership")
	}
}

func TestStepPreservesCallerDeadline(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Second, time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			ctx := t.Context()
			if timeout != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			m := &deadlineMemory{Memory: newStepMemory(), t: t, ctx: ctx}
			core, err := cortexm.Acquire(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			if err := core.Halt(ctx); err != nil {
				t.Fatal(err)
			}
			if err := core.Step(ctx); err != nil {
				t.Fatal(err)
			}
			if err := core.Release(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
