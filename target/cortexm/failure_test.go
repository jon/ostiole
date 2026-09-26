package cortexm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestControlReadFailuresRetainCleanup(t *testing.T) {
	for fail := 1; fail <= 6; fail++ {
		m := newControlMemory()
		m.failRead = fail
		core, err := cortexm.Acquire(t.Context(), m)
		if err == nil {
			err = core.Halt(t.Context())
		}
		if err == nil {
			err = core.Resume(t.Context())
		}
		if !errors.Is(err, errMemory) {
			t.Fatalf("read %d: %v", fail, err)
		}
		if core != nil {
			if err := core.Release(t.Context()); err != nil {
				t.Fatalf("read %d cleanup: %v", fail, err)
			}
		}
		if m.control&debugEnable != 0 || m.halted {
			t.Fatalf("read %d left control=%#x halted=%v", fail, m.control, m.halted)
		}
	}
}

func TestResumeFailureRetainsCleanup(t *testing.T) {
	for _, after := range []bool{false, true} {
		m := newControlMemory()
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
		m.failWrite, m.afterWrite = m.writes+1, after
		if err := core.Resume(t.Context()); !errors.Is(err, errMemory) {
			t.Fatal(err)
		}
		if err := core.Halt(t.Context()); err == nil {
			t.Fatal("halt during cleanup")
		}
		err = core.Release(t.Context())
		if !after {
			if err == nil {
				t.Fatal("replayed uncertain resume")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if m.halted || m.control != 0 {
			t.Fatalf("restored halted=%v control=%#x", m.halted, m.control)
		}
	}
}

func TestCanceledReleaseKeepsTargetUnavailable(t *testing.T) {
	m := newControlMemory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := core.Release(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := core.Halt(t.Context()); err == nil {
		t.Fatal("halt after release started")
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := core.Resume(t.Context()); err == nil {
		t.Fatal("resume after release")
	}
}

func TestAcquireDetectsIgnoredWrite(t *testing.T) {
	m := newControlMemory()
	m.ignoreWrites = true
	if core, err := cortexm.Acquire(t.Context(), m); err == nil || core != nil {
		t.Fatalf("core=%v err=%v", core, err)
	}
}

func TestReleaseRefusesUnsafeExternalModeChange(t *testing.T) {
	m := newControlMemory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	m.control |= 8
	writes := m.writes
	if err := core.Release(t.Context()); err == nil || m.writes != writes {
		t.Fatal("release changed live interrupt masking")
	}
	m.control &^= 8
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseAfterResumePreservesNewHalt(t *testing.T) {
	m := newControlMemory()
	m.control = debugEnable
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := core.Resume(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.control, m.halted = debugEnable|haltRequest, true
	writes := m.writes
	if err := core.Release(t.Context()); err != nil || !m.halted || m.writes != writes {
		t.Fatal("release resumed a new unowned halt")
	}
}
