package cortexm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

func newM33StepMemory() *stepMemory {
	m := newStepMemory()
	m.cpuid, m.status = 0x411fd210, secureDebug
	return m
}

func TestM33StepReturnsOwnedHalt(t *testing.T) {
	for _, delay := range []int{0, 2} {
		m := newM33StepMemory()
		m.stepDelay = delay
		core := acquireStep(t, m)
		for range 3 {
			if err := core.Step(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := core.ReadRegister(t.Context(), cortexm.PC); err != nil {
				t.Fatal(err)
			}
		}
		if m.steps != 3 || m.launches != 3 || m.control != debugEnable|haltRequest || !m.halted {
			t.Fatal("step did not return normalized owned halt")
		}
		if err := core.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := core.Release(t.Context()); err != nil || m.control != 0 || m.halted {
			t.Fatal("restoration failed", err)
		}
	}
}

func TestM33StepRejectsUnownedOrUnsafeLaunch(t *testing.T) {
	for _, change := range []string{"inherited", "restart", "permission", "snap", "event"} {
		t.Run(change, func(t *testing.T) {
			m := newM33StepMemory()
			var core *cortexm.Target
			if change == "inherited" {
				m.control, m.halted = debugEnable|haltRequest, true
				var err error
				core, err = cortexm.Acquire(t.Context(), m)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				core = acquireStep(t, m)
			}
			switch change {
			case "restart":
				m.status |= 1 << 26
			case "permission":
				m.status = 0
			case "snap":
				m.control |= 32
			case "event":
				m.reasons |= 16
			}
			writes := m.writes
			if err := core.Step(t.Context()); err == nil || m.writes != writes || m.launches != 0 {
				t.Fatal("unsafe step launched")
			}
		})
	}
}

func TestM33StepCompletionRejectsPermissionAndSnap(t *testing.T) {
	for _, change := range []string{"permission", "snap", "snap and mask"} {
		t.Run(change, func(t *testing.T) {
			m := newM33StepMemory()
			core := acquireStep(t, m)
			m.onStep = func() {
				switch change {
				case "permission":
					m.status &^= secureDebug
				case "snap":
					m.control |= 32
				case "snap and mask":
					m.control |= 32 | 8
				}
			}
			writes := m.writes
			if err := core.Step(t.Context()); err == nil || m.writes != writes+1 {
				t.Fatal("unsafe completion normalized", err)
			}
			writes = m.writes
			if err := core.Release(t.Context()); err == nil || m.writes != writes {
				t.Fatal("unsafe cleanup wrote control", err)
			}
			m.control &^= 32 | 8
			m.status = secureDebug
			m.onStep = nil
			err := core.Release(t.Context())
			if change == "permission" {
				if err != nil || m.halted {
					t.Fatal("permission recovery failed", err)
				}
			} else if err == nil || m.writes != writes || !m.halted {
				t.Fatal("snap clearing allowed cleanup", err)
			}
		})
	}
}

func TestM33StepRestartAfterCompletionBlocksCleanup(t *testing.T) {
	for _, phase := range []string{"reason", "normalization", "during reason"} {
		t.Run(phase, func(t *testing.T) {
			m := newM33StepMemory()
			core := acquireStep(t, m)
			switch phase {
			case "reason":
				m.failRead = m.reads + 4
			case "normalization":
				m.failWrite = m.writes + 2
			default:
				n := m.reads + 3
				m.onRead = func() {
					if m.reads == n {
						m.status |= 1 << 26
					}
				}
			}
			if err := core.Step(t.Context()); err == nil {
				t.Fatal("completion failure ignored")
			}
			if m.launches != 1 || m.steps != 1 {
				t.Fatal("step did not execute")
			}
			writes := m.writes
			m.status |= 1 << 26
			m.onRead = nil
			if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
				t.Fatal("new halt mistaken for completed step")
			}
		})
	}
}

func TestM33StepCancellationSettlesWithoutReplay(t *testing.T) {
	m := newM33StepMemory()
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
		t.Fatal("cleanup changed running step")
	}
	m.blockStep = false
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.steps != 1 || m.launches != 1 || m.halted || m.control != 0 {
		t.Fatal("cleanup replayed step or lost restoration")
	}
}

func TestM33StepPreservesCompetingEvents(t *testing.T) {
	for _, before := range []bool{false, true} {
		for _, reason := range []uint32{2, 4, 8, 16} {
			m := newM33StepMemory()
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
					t.Fatal("launched with stale event")
				}
			} else {
				writes = m.writes
				if err := core.Resume(t.Context()); err == nil || m.writes != writes {
					t.Fatal("competing halt resumed")
				}
				if err := core.Release(t.Context()); err != nil || !m.halted {
					t.Fatal("competing halt not preserved", err)
				}
			}
			if m.reasons&reason == 0 {
				t.Fatal("cleared competing evidence")
			}
		}
	}
}

func TestM33StepUncertainLaunchNeverReplays(t *testing.T) {
	for _, after := range []bool{false, true} {
		m := newM33StepMemory()
		core := acquireStep(t, m)
		m.failWrite, m.afterWrite = m.writes+1, after
		if err := core.Step(t.Context()); !errors.Is(err, errMemory) {
			t.Fatal(err)
		}
		writes := m.writes
		if err := core.Release(t.Context()); err == nil || m.writes != writes {
			t.Fatal("uncertain launch resumed or replayed")
		}
	}
}

func TestM33StepFailedNormalizationRetainsOwnership(t *testing.T) {
	for _, after := range []bool{false, true} {
		m := newM33StepMemory()
		core := acquireStep(t, m)
		m.failWrite, m.afterWrite = m.writes+2, after
		if err := core.Step(t.Context()); !errors.Is(err, errMemory) {
			t.Fatal(err)
		}
		if err := core.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if m.steps != 1 || m.launches != 1 || m.halted || m.control != 0 {
			t.Fatal("normalization replayed execution or failed restoration")
		}
	}
}

func TestM33StepPermissionFailureRetainsCompletionBoundary(t *testing.T) {
	m := newM33StepMemory()
	core := acquireStep(t, m)
	m.onStep = func() { m.status &^= secureDebug }
	if err := core.Step(t.Context()); err == nil {
		t.Fatal("lost permission accepted")
	}
	if m.steps != 1 || m.launches != 1 {
		t.Fatal("step did not complete")
	}
	writes := m.writes
	m.onStep = nil
	m.status = secureDebug | 1<<26
	if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
		t.Fatal("permission recovery claimed a later halt", err)
	}
}

func TestM33StepMayEnterException(t *testing.T) {
	m := newM33StepMemory()
	core := acquireStep(t, m)
	m.onStep = func() { m.registers[15] = 0x100; m.registers[16] = 0x0100000f }
	if err := core.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	pc, err := core.ReadRegister(t.Context(), cortexm.PC)
	if err != nil || pc != 0x100 {
		t.Fatal("exception entry rejected", err)
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
