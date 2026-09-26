package cortexm_test

import (
	"fmt"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestLostHaltRequestPreservesIndependentStop(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable} {
		for _, stopped := range []bool{false, true} {
			t.Run(fmt.Sprintf("debug=%d/stopped=%t", initial, stopped), func(t *testing.T) {
				checkLostHaltRequest(t, initial, stopped)
			})
		}
	}
}

func checkLostHaltRequest(t *testing.T, initial uint32, stopped bool) {
	t.Helper()
	m := newControlMemory()
	m.control = initial
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	m.onWrite = func() {
		m.control, m.halted = debugEnable, stopped
	}
	if err := core.Halt(t.Context()); err == nil {
		t.Fatal("lost halt request was not reported")
	}
	m.onWrite = nil
	m.halted = true
	writes := m.writes
	err = core.Release(t.Context())
	if m.writes != writes || !m.halted {
		t.Fatal("cleanup resumed an independent halt after losing its request")
	}
	if initial == debugEnable && err != nil {
		t.Fatalf("release with no remaining control changes: %v", err)
	}
	if initial == 0 && err == nil {
		t.Fatal("disabled debug despite an independent halt")
	}
	m.halted = false
	if err := core.Release(t.Context()); err != nil || m.control != initial {
		t.Fatalf("release retry=%v control=%#x", err, m.control)
	}
}

func TestLaterObservationRelinquishesLostHaltRequest(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable} {
		for _, status := range []bool{false, true} {
			t.Run(fmt.Sprintf("debug=%d/status=%t", initial, status), func(t *testing.T) {
				checkLaterHaltLoss(t, initial, status)
			})
		}
	}
}

func checkLaterHaltLoss(t *testing.T, initial uint32, status bool) {
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
	m.control, m.halted = debugEnable, false
	writes := m.writes
	if status {
		if halted, err := core.Halted(t.Context()); err != nil || halted {
			t.Fatalf("halted=%t err=%v", halted, err)
		}
		m.control, m.halted = debugEnable|haltRequest, true
		if err := core.Resume(t.Context()); err == nil || m.writes != writes {
			t.Fatal("resume retained ownership after observing the lost request")
		}
	}
	m.halted = true
	err = core.Release(t.Context())
	if m.writes != writes || !m.halted {
		t.Fatal("release resumed a stop after observing the lost request")
	}
	if initial == debugEnable && err != nil {
		t.Fatal(err)
	}
	if initial == 0 && err == nil {
		t.Fatal("disabled debug despite an independent halt")
	}
}
