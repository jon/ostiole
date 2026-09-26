package cortexm_test

import (
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestCompletedResumeNeverReleasesNewHalt(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, delay := range []int{1, 3} {
			for _, initial := range []uint32{0, debugEnable} {
				checkNewHalt(t, cleanup, delay, initial)
			}
		}
	}
}

func checkNewHalt(t *testing.T, cleanup bool, delay int, initial uint32) {
	t.Helper()
	m := newControlMemory()
	m.control = initial
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.rehaltAfter = delay
	if cleanup {
		err = core.Release(t.Context())
	} else {
		err = core.Resume(t.Context())
	}
	if err == nil {
		t.Fatal("new debug event was not reported")
	}
	writes := m.writes
	err = core.Release(t.Context())
	if initial == debugEnable && err != nil {
		t.Fatal(err)
	}
	if initial == 0 && err == nil {
		t.Fatal("disabled debug despite new halt")
	}
	if m.writes != writes || !m.halted {
		t.Fatalf("replayed resume: initial=%d cleanup=%v delay=%d", initial, cleanup, delay)
	}
}

func TestUncertainResumeDoesNotReplayWhileHalted(t *testing.T) {
	m := newControlMemory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.failWrite = m.writes + 1
	if err := core.Resume(t.Context()); err == nil {
		t.Fatal("resume succeeded")
	}
	writes := m.writes
	if err := core.Release(t.Context()); err == nil || m.writes != writes {
		t.Fatal("uncertain resume was replayed")
	}
	// A later running observation resolves the uncertainty without another run request.
	m.control, m.halted = debugEnable, false
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
