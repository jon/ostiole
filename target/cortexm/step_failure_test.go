package cortexm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

func TestStepPreservesCompetingEvents(t *testing.T) {
	for _, before := range []bool{false, true} {
		for _, reason := range []uint32{2, 4, 8, 16} {
			m := newStepMemory()
			m.control = debugEnable
			core := acquireStep(t, m)
			if before {
				m.reasons |= reason
			} else {
				m.onStep = func() { m.reasons |= reason }
			}
			writes := m.writes
			if err := core.Step(t.Context()); err == nil {
				t.Fatal("competing event accepted")
			}
			if before {
				if writes != m.writes || m.launches != 0 {
					t.Fatal("stepped with stale competing event")
				}
			} else {
				writes = m.writes
				if err := core.Resume(t.Context()); err == nil || writes != m.writes {
					t.Fatal("resumed competing stop")
				}
				if err := core.Release(t.Context()); err != nil || !m.halted || m.control != debugEnable|haltRequest {
					t.Fatalf("release: %v", err)
				}
			}
			if m.reasons&reason == 0 {
				t.Fatal("cleared competing evidence")
			}
		}
	}
}

func TestStepLaunchIsNeverReplayed(t *testing.T) {
	for _, after := range []bool{false, true} {
		m := newStepMemory()
		core := acquireStep(t, m)
		m.failWrite, m.afterWrite = m.writes+1, after
		if err := core.Step(t.Context()); !errors.Is(err, errMemory) {
			t.Fatal(err)
		}
		writes := m.writes
		if err := core.Release(t.Context()); err == nil || m.writes != writes {
			t.Fatal("uncertain launch cleaned up")
		}
		if err := core.Step(t.Context()); err == nil || m.writes != writes {
			t.Fatal("uncertain launch replayed")
		}
	}
}

func TestStepIgnoredLaunchIsNotCompletion(t *testing.T) {
	m := newStepMemory()
	core := acquireStep(t, m)
	m.ignoreWrites = true
	if err := core.Step(t.Context()); err == nil {
		t.Fatal("ignored step succeeded")
	}
	writes := m.writes
	m.ignoreWrites = false
	if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
		t.Fatal("ignored step resumed")
	}
}

func TestStepCompletionFailuresRetryCleanup(t *testing.T) {
	for _, phase := range []string{"poll", "reason", "normalize-before", "normalize-after", "confirm", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			m := newStepMemory()
			core := acquireStep(t, m)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := errMemory
			switch phase {
			case "poll":
				m.failRead = m.reads + 3
			case "reason":
				m.failRead = m.reads + 4
			case "normalize-before", "normalize-after":
				m.failWrite = m.writes + 2
				m.afterWrite = phase == "normalize-after"
			case "confirm":
				m.failRead = m.reads + 5
			case "cancel":
				m.onStep = cancel
				want = context.Canceled
			}
			if err := core.Step(ctx); !errors.Is(err, want) {
				t.Fatalf("step: %v", err)
			}
			if _, err := core.ReadRegister(t.Context(), cortexm.PC); err == nil {
				t.Fatal("ordinary call after failure")
			}
			m.onStep = nil
			if err := core.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if m.launches != 1 || m.steps != 1 || m.control != 0 || m.halted {
				t.Fatal("failed cleanup or replay")
			}
		})
	}
}

func TestStepLostStateBlocksCleanup(t *testing.T) {
	for _, change := range []string{"reset", "debug", "mask"} {
		m := newStepMemory()
		core := acquireStep(t, m)
		m.onStep = func() {
			switch change {
			case "reset":
				m.reset = true
			case "debug":
				m.control = 0
			case "mask":
				m.control |= 8
			}
		}
		if err := core.Step(t.Context()); err == nil {
			t.Fatal("lost state accepted")
		}
		writes := m.writes
		m.control, m.halted = debugEnable|stepRequest|haltRequest, true
		if err := core.Release(t.Context()); err == nil || m.writes != writes {
			t.Fatal("lost step state restored")
		}
	}
}

func TestStepWaitsForRegisterTransfer(t *testing.T) {
	m := newStepMemory()
	core := acquireStep(t, m)
	m.pending, m.block = true, true
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	err := core.Step(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || m.launches != 0 {
		t.Fatal("step passed busy register transfer")
	}
	m.block = false
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStepNormalizationCanBeRetried(t *testing.T) {
	m := newStepMemory()
	core := acquireStep(t, m)
	m.onStep = func() { m.ignoreWrites = true }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	err := core.Step(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || !m.halted {
		t.Fatal(err)
	}
	m.ignoreWrites = false
	m.onStep = nil
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.steps != 1 || m.launches != 1 || m.halted {
		t.Fatal("normalization replayed step or failed cleanup")
	}
}

func TestStepCompetingHaltPreventsDisabledDebugRestoration(t *testing.T) {
	m := newStepMemory()
	core := acquireStep(t, m)
	m.onStep = func() { m.reasons |= 2 }
	if err := core.Step(t.Context()); err == nil {
		t.Fatal("competing halt accepted")
	}
	writes := m.writes
	if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
		t.Fatal("resumed competing stop to restore debug")
	}
	m.control, m.halted = debugEnable, false
	if err := core.Release(t.Context()); err != nil || m.control != 0 {
		t.Fatalf("later cleanup: %v", err)
	}
}

func TestStepMayEnterAnException(t *testing.T) {
	m := newStepMemory()
	core := acquireStep(t, m)
	m.onStep = func() { m.registers[15] = 0x100; m.registers[16] = 0x0100000f }
	if err := core.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pc, err := core.ReadRegister(t.Context(), cortexm.PC); err != nil || pc != 0x100 {
		t.Fatal("exception entry rejected")
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStepLosingCompletedHaltBlocksCleanup(t *testing.T) {
	for _, reasonRead := range []bool{false, true} {
		t.Run("lost-halt", func(t *testing.T) {
			m := newStepMemory()
			core := acquireStep(t, m)
			if reasonRead {
				m.failRead = m.reads + 4
			} else {
				m.failWrite = m.writes + 2
			}
			if err := core.Step(t.Context()); !errors.Is(err, errMemory) {
				t.Fatal(err)
			}
			m.control, m.halted = debugEnable|stepRequest, false
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
			err := core.Release(ctx)
			cancel()
			if err == nil {
				t.Fatal("accepted running state during restoration")
			}
			writes := m.writes
			m.control, m.halted, m.reasons = debugEnable|stepRequest|haltRequest, true, 2
			if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
				t.Fatal("cleanup claimed a later independent stop")
			}
		})
	}
}
