package cortexm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

func TestRegisterReadFailures(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		for _, phase := range []string{"status", "selector-before", "selector-after", "poll", "data", "cancel"} {
			t.Run(fmt.Sprintf("inherited=%t/%s", inherited, phase), func(t *testing.T) {
				checkReadFailure(t, inherited, phase)
			})
		}
	}
}

func checkReadFailure(t *testing.T, inherited bool, phase string) {
	t.Helper()
	m := newRegisterMemory()
	core := acquireRegisters(t, m, inherited)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	switch phase {
	case "status":
		m.failRead = m.reads + 1
	case "selector-before", "selector-after":
		m.failWrite, m.afterWrite = m.writes+1, phase == "selector-after"
	case "poll":
		m.failRead = m.reads + 2
	case "data":
		m.failRead = m.reads + 3
	case "cancel":
		m.afterSelector = cancel
	}
	value, err := core.ReadRegister(ctx, cortexm.R4)
	expected := errMemory
	if phase == "cancel" {
		expected = context.Canceled
	}
	if !errors.Is(err, expected) || value != 0 {
		t.Fatalf("value=%#x error=%v", value, err)
	}
	transfers := m.transfers
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.transfers != transfers || m.halted != inherited {
		t.Fatal("cleanup replayed transfer or changed inherited halt")
	}
}

func TestRegisterReadInheritsBusyTransfer(t *testing.T) {
	m := newRegisterMemory()
	core := acquireRegisters(t, m, true)
	m.pending, m.block = true, true
	writes := m.writes
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	_, err := core.ReadRegister(ctx, cortexm.R0)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || m.writes != writes {
		t.Fatal("overwrote inherited transfer")
	}
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Millisecond)
	err = core.Release(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("released inherited busy transfer")
	}
	m.block = false
	if err := core.Release(t.Context()); err != nil || m.writes != writes || !m.halted {
		t.Fatal("cleanup changed inherited state")
	}
}

func TestRegisterReadCancellationBeforeSelector(t *testing.T) {
	m := newRegisterMemory()
	core := acquireRegisters(t, m, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.onRead = cancel
	writes := m.writes
	if _, err := core.ReadRegister(ctx, cortexm.R0); !errors.Is(err, context.Canceled) || m.writes != writes {
		t.Fatal("canceled read issued selector")
	}
	m.onRead = nil
	if _, err := core.ReadRegister(t.Context(), cortexm.R0); err != nil {
		t.Fatal(err)
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPendingRegisterTransferCannotSurviveLostState(t *testing.T) {
	for _, change := range []string{"running", "disabled", "reset"} {
		m := newRegisterMemory()
		core := acquireRegisters(t, m, false)
		m.afterSelector = func() {
			switch change {
			case "running":
				m.halted = false
			case "disabled":
				m.control = 0
			case "reset":
				m.reset = true
			}
		}
		if _, err := core.ReadRegister(t.Context(), cortexm.R0); err == nil {
			t.Fatal("lost state returned data")
		}
		writes := m.writes
		m.control, m.halted = debugEnable|haltRequest, true
		if err := core.Release(t.Context()); err == nil || m.writes != writes {
			t.Fatal("later halt disguised lost transfer")
		}
	}
}
