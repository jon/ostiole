package cortexm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

const secureDebug = uint32(1 << 20)

func newM33Memory() *controlMemory {
	m := newControlMemory()
	m.cpuid, m.status = 0x411fd210, secureDebug
	return m
}

func TestM33ControlRestoresInheritedState(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable, debugEnable | haltRequest} {
		m := newM33Memory()
		m.control, m.halted = initial, initial&haltRequest != 0
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if core.Identity().Raw != m.cpuid || m.halted != (initial&haltRequest != 0) {
			t.Fatal("acquisition changed identity or halt state")
		}
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
		halted, err := core.Halted(t.Context())
		if err != nil || !halted {
			t.Fatalf("Halted = %v, %v", halted, err)
		}
		err = core.Resume(t.Context())
		if initial&haltRequest != 0 {
			if err == nil || !m.halted {
				t.Fatal("resumed inherited halt")
			}
		} else {
			if err != nil || m.halted {
				t.Fatalf("Resume = %v, halted=%v", err, m.halted)
			}
			if err := core.Halt(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if err := core.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if m.control != initial || m.halted != (initial&haltRequest != 0) {
			t.Fatalf("restored %#x halted=%v", m.control, m.halted)
		}
	}
}

func TestM33AcquireRejectsUnsupportedStateBeforeWrites(t *testing.T) {
	for _, test := range []struct {
		name            string
		status, control uint32
	}{
		{"secure debug denied", 0, 0},
		{"denied while enabled", 0, debugEnable},
		{"snap stall", secureDebug, debugEnable | haltRequest | 32},
		{"step", secureDebug, debugEnable | 4},
		{"mask interrupts", secureDebug, debugEnable | 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newM33Memory()
			m.status, m.control = test.status, test.control
			m.halted = test.control&haltRequest != 0
			core, err := cortexm.Acquire(t.Context(), m)
			if err == nil || core != nil || m.writes != 0 {
				t.Fatalf("core=%v err=%v writes=%d", core, err, m.writes)
			}
		})
	}
}

func TestM33AcquireFailureRestoresDebug(t *testing.T) {
	for _, after := range []bool{false, true} {
		m := newM33Memory()
		m.failWrite, m.afterWrite = 1, after
		core, err := cortexm.Acquire(t.Context(), m)
		if core != nil || !errors.Is(err, errMemory) || m.control != 0 {
			t.Fatalf("core=%v err=%v control=%#x", core, err, m.control)
		}
	}
}

func TestM33RejectsControlChangesBeforeResume(t *testing.T) {
	for _, snap := range []bool{false, true} {
		m := newM33Memory()
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
		if snap {
			m.control |= 32
		} else {
			m.status = 0
		}
		writes := m.writes
		if err := core.Resume(t.Context()); err == nil || m.writes != writes {
			t.Fatalf("Resume = %v, writes=%d", err, m.writes-writes)
		}
		if err := core.Release(t.Context()); err == nil || m.writes != writes {
			t.Fatalf("Release = %v, writes=%d", err, m.writes-writes)
		}
		if snap {
			m.control &^= 32
			if err := core.Release(t.Context()); err == nil || m.writes != writes {
				t.Fatal("clearing snap-stall allowed unsafe restoration")
			}
		}
		if !snap {
			m.status = secureDebug
			if err := core.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if m.control != 0 || m.halted {
				t.Fatal("restoration failed after permission returned")
			}
		}
	}
}

func TestM33HaltHonorsCancellationAndAllowsCleanup(t *testing.T) {
	m := newM33Memory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	m.stall = true
	m.onWrite = cancel
	if err := core.Halt(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Halt = %v", err)
	}
	m.stall = false
	m.onWrite = nil
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.control != 0 || m.halted {
		t.Fatal("canceled halt cleanup failed")
	}
}

func TestM33DeniedReadbackRetainsCleanup(t *testing.T) {
	m := newM33Memory()
	m.onWrite = func() { m.status = 0 }
	core, err := cortexm.Acquire(t.Context(), m)
	if err == nil || core == nil || m.writes != 1 {
		t.Fatalf("core=%v err=%v writes=%d", core, err, m.writes)
	}
	reads := m.reads
	if _, err := core.Halted(t.Context()); err == nil || m.reads != reads {
		t.Fatal("ordinary call during failed acquisition cleanup")
	}
	m.onWrite = nil
	m.status = secureDebug
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.control != 0 {
		t.Fatal("did not restore disabled debug")
	}
}

func TestM33SnapStallRemainsLatchedWithOtherControlErrors(t *testing.T) {
	for _, other := range []uint32{4, 8} {
		m := newM33Memory()
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
		m.control |= 32 | other
		if _, err := core.Halted(t.Context()); err == nil {
			t.Fatal("accepted changed control")
		}
		m.control &^= 32 | other
		writes := m.writes
		if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
			t.Fatalf("Release = %v, writes=%d halted=%v", err, m.writes-writes, m.halted)
		}
	}
}

func TestM33RestartRelinquishesHaltOwnership(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable} {
		for _, other := range []uint32{0, 4, 8} {
			m := newM33Memory()
			m.control = initial
			core, err := cortexm.Acquire(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			if err := core.Halt(t.Context()); err != nil {
				t.Fatal(err)
			}
			m.status |= 1 << 26
			m.control |= other
			_, err = core.Halted(t.Context())
			if (err != nil) != (other != 0) {
				t.Fatalf("Halted with control %#x: %v", other, err)
			}
			m.control &^= other
			writes := m.writes
			err = core.Release(t.Context())
			if m.writes != writes || !m.halted {
				t.Fatal("resumed a new halt after restart")
			}
			if (err != nil) != (initial == 0) {
				t.Fatalf("Release initial=%#x: %v", initial, err)
			}
		}
	}
}

func TestM33RestoreChecksFinalReadPermission(t *testing.T) {
	m := newM33Memory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	m.onRead = func() { m.status = 0 }
	writes := m.writes
	if err := core.Release(t.Context()); err == nil || m.writes != writes {
		t.Fatalf("Release = %v, writes=%d", err, m.writes-writes)
	}
	m.onRead = nil
	m.status = secureDebug
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.control != 0 {
		t.Fatal("did not restore debug after permission returned")
	}
}
