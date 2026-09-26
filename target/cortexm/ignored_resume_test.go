package cortexm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestIgnoredResumeRetainsCleanup(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable} {
		for _, cleanup := range []bool{false, true} {
			for _, failure := range []string{"none", "read", "cancel"} {
				t.Run(fmt.Sprintf("debug=%d/cleanup=%t/failure=%s", initial, cleanup, failure), func(t *testing.T) {
					checkIgnoredResume(t, initial, cleanup, failure)
				})
			}
		}
	}
}

func checkIgnoredResume(t *testing.T, initial uint32, cleanup bool, failure string) {
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
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if failure == "read" {
		m.failRead = m.reads + 1
		if cleanup {
			m.failRead++
		}
	}
	if failure == "cancel" {
		m.onWrite = cancel
	}
	m.ignoreWrites = true
	if cleanup {
		err = core.Release(ctx)
	} else {
		err = core.Resume(ctx)
	}
	if err == nil {
		t.Fatal("ignored resume was not reported")
	}
	if failure == "read" && !errors.Is(err, errMemory) {
		t.Fatalf("read failure = %v", err)
	}
	if failure == "cancel" && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	m.onWrite = nil
	writes := m.writes
	if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
		t.Fatalf("release=%v writes=%d halted=%t", err, m.writes, m.halted)
	}
	m.ignoreWrites = false
	m.control, m.halted = debugEnable, false
	if err := core.Release(t.Context()); err != nil || m.control != initial {
		t.Fatalf("resolved release=%v control=%#x", err, m.control)
	}
}

func TestLaterResumeConfirmationPreservesIndependentHalt(t *testing.T) {
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
		m.failRead = m.reads + 1
		if err := core.Resume(t.Context()); !errors.Is(err, errMemory) {
			t.Fatal(err)
		}
		m.halted = true
		writes := m.writes
		err = core.Release(t.Context())
		if m.writes != writes || !m.halted {
			t.Fatal("cleanup resumed an independent halt")
		}
		if initial == debugEnable && err != nil {
			t.Fatal(err)
		}
		if initial == 0 && err == nil {
			t.Fatal("disabled debug despite an independent halt")
		}
		m.halted = false
		if err := core.Release(t.Context()); err != nil || m.control != initial {
			t.Fatalf("release retry=%v control=%#x", err, m.control)
		}
	}
}
