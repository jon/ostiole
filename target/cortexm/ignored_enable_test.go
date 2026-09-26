package cortexm_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestAcquireIgnoredEnableNeedsNoRestoration(t *testing.T) {
	for _, initial := range []uint32{0, 14} {
		t.Run(fmt.Sprintf("control=%d", initial), func(t *testing.T) {
			m := newControlMemory()
			m.control, m.ignoreWrites, m.failWrite = initial, true, 2
			core, err := cortexm.Acquire(t.Context(), m)
			if core != nil || err == nil || errors.Is(err, errMemory) {
				t.Fatalf("core=%v err=%v", core, err)
			}
			if m.reads != 3 || m.writes != 1 || m.control != initial {
				t.Fatalf("reads=%d writes=%d control=%#x", m.reads, m.writes, m.control)
			}
		})
	}
}

func TestAcquireFailedEnableNeedsNoRestoration(t *testing.T) {
	for _, initial := range []uint32{0, 14} {
		t.Run(fmt.Sprintf("control=%d", initial), func(t *testing.T) {
			m := newControlMemory()
			m.control, m.rejectWrites = initial, true
			core, err := cortexm.Acquire(t.Context(), m)
			if core != nil || !errors.Is(err, errMemory) {
				t.Fatalf("core=%v err=%v", core, err)
			}
			if m.reads != 3 || m.writes != 1 || m.control != initial {
				t.Fatalf("reads=%d writes=%d control=%#x", m.reads, m.writes, m.control)
			}
		})
	}
}

func TestReleaseRetryConfirmsDisabledDebugWithoutWrite(t *testing.T) {
	m := newControlMemory()
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	m.failWrite, m.afterWrite = 2, true
	if err := core.Release(t.Context()); !errors.Is(err, errMemory) {
		t.Fatalf("first release = %v", err)
	}
	m.failRead = m.reads + 1
	if err := core.Release(t.Context()); !errors.Is(err, errMemory) || m.writes != 2 {
		t.Fatalf("failed confirmation: err=%v writes=%d", err, m.writes)
	}
	m.failRead, m.rejectWrites = 0, true
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.writes != 2 || m.control != 0 {
		t.Fatalf("writes=%d control=%#x", m.writes, m.control)
	}
}
