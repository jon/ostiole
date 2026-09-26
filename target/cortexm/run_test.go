package cortexm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

func TestHaltResumeAndRelease(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable} {
		m := newControlMemory()
		m.control = initial
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
		if halted, err := core.Halted(t.Context()); err != nil || !halted {
			t.Fatalf("halted=%v err=%v", halted, err)
		}
		if err := core.Resume(t.Context()); err != nil || m.halted {
			t.Fatalf("resume=%v halted=%v", err, m.halted)
		}
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := core.Release(t.Context()); err != nil || m.halted || m.control != initial {
			t.Fatalf("release=%v control=%#x halted=%v", err, m.control, m.halted)
		}
	}
}

func TestInheritedHaltCannotBeResumed(t *testing.T) {
	for _, control := range []uint32{debugEnable, debugEnable | haltRequest} {
		m := newControlMemory()
		m.control, m.halted = control, true
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
		if halted, err := core.Halted(t.Context()); err != nil || !halted {
			t.Fatalf("halted=%t err=%v", halted, err)
		}
		if err := core.Resume(t.Context()); err == nil {
			t.Fatal("resumed inherited halt")
		}
		if err := core.Release(t.Context()); err != nil || !m.halted || m.control != control || m.writes != 0 {
			t.Fatalf("release=%v halted=%v control=%#x writes=%d", err, m.halted, m.control, m.writes)
		}
	}
}

func TestHaltReadbackFailureRequiresRelease(t *testing.T) {
	m := newControlMemory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	m.failRead, m.afterWrite = m.reads+2, true
	if err := core.Halt(t.Context()); !errors.Is(err, errMemory) {
		t.Fatal(err)
	}
	reads, writes := m.reads, m.writes
	if err := core.Resume(t.Context()); err == nil {
		t.Fatal("resume after uncertain halt succeeded")
	}
	if _, err := core.Halted(t.Context()); err == nil {
		t.Fatal("status after uncertain halt succeeded")
	}
	if reads != m.reads || writes != m.writes {
		t.Fatal("ordinary traffic during cleanup")
	}
	m.failWrite = m.writes + 1
	if err := core.Release(t.Context()); !errors.Is(err, errMemory) {
		t.Fatal(err)
	}
	if err := core.Release(t.Context()); err != nil || m.control != 0 || m.halted {
		t.Fatalf("release=%v control=%#x", err, m.control)
	}
}

func TestControlCancellationBeforeTrafficAndPolling(t *testing.T) {
	m := newControlMemory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reads, writes := m.reads, m.writes
	if err := core.Halt(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if reads != m.reads || writes != m.writes {
		t.Fatal("canceled halt reached memory")
	}
	m.stall = true
	ctx, cancel = context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := core.Halt(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("halt = %v", err)
	}
	m.stall = false
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestInactiveTargetRejectsControl(t *testing.T) {
	for _, core := range []*cortexm.Target{nil, {}} {
		if err := core.Halt(t.Context()); err == nil {
			t.Fatal("inactive halt")
		}
		if err := core.Resume(t.Context()); err == nil {
			t.Fatal("inactive resume")
		}
		if _, err := core.Halted(t.Context()); err == nil {
			t.Fatal("inactive status")
		}
		if err := core.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
