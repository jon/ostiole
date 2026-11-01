package cortexm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

type canceledPreflightMemory struct {
	*stepMemory
	address uint32
	cancel  context.CancelFunc
	extra   error
}

func (m *canceledPreflightMemory) ReadWord(ctx context.Context, address uint32) (uint32, error) {
	if m.cancel != nil && address == m.address {
		m.cancel()
		m.cancel = nil
		return 0, errors.Join(ctx.Err(), m.extra)
	}
	return m.stepMemory.ReadWord(ctx, address)
}

func TestCanceledPreflightReadKeepsTargetUsable(t *testing.T) {
	for _, m33 := range []bool{false, true} {
		for _, operation := range []string{"halt", "status", "read register", "write register", "step status", "step reason"} {
			t.Run(fmt.Sprintf("M33=%t/%s", m33, operation), func(t *testing.T) { exerciseCanceledPreflightRead(t, m33, operation) })
		}
	}
}

func exerciseCanceledPreflightRead(t *testing.T, m33 bool, operation string) {
	t.Helper()
	memory := newStepMemory()
	if m33 {
		memory = newM33StepMemory()
	}
	m := &canceledPreflightMemory{stepMemory: memory, address: dhcsr}
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if operation != "halt" && operation != "status" {
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if operation == "step reason" {
		m.address = 0xe000ed30
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.cancel = cancel
	writes := m.writes
	err = callPreflightOperation(ctx, core, operation)
	if !errors.Is(err, context.Canceled) || m.writes != writes {
		t.Fatalf("preflight=%v, writes=%d/%d", err, m.writes, writes)
	}
	if _, err := core.Halted(t.Context()); err != nil {
		t.Fatalf("preflight cancellation made target unavailable: %v", err)
	}
	lostClaim := m33 && operation != "halt" && operation != "status" && operation != "step reason"
	checkCanceledPreflightRelease(t, core, m, writes, lostClaim)
}

func checkCanceledPreflightRelease(t *testing.T, core *cortexm.Target, m *canceledPreflightMemory, writes int, lostClaim bool) {
	t.Helper()
	if lostClaim {
		if err := core.Resume(t.Context()); err == nil || m.writes != writes {
			t.Fatal("failed DHCSR read retained an unprovable M33 halt claim")
		}
		if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
			t.Fatal("restored disabled debug over an unowned halt")
		}
		m.control, m.halted = debugEnable, false
	}
	if err := core.Release(t.Context()); err != nil || m.halted || m.control != 0 {
		t.Fatalf("release=%v, control=%#x", err, m.control)
	}
}

func callPreflightOperation(ctx context.Context, core *cortexm.Target, operation string) error {
	switch operation {
	case "halt":
		return core.Halt(ctx)
	case "status":
		_, err := core.Halted(ctx)
		return err
	case "read register":
		_, err := core.ReadRegister(ctx, cortexm.R0)
		return err
	case "write register":
		return core.WriteRegister(ctx, cortexm.R0, 42)
	default:
		return core.Step(ctx)
	}
}

func TestMixedPreflightReadFailureRetainsCleanupOnlyState(t *testing.T) {
	for _, operation := range []string{"halt", "read register", "step reason"} {
		t.Run(operation, func(t *testing.T) {
			memory := newStepMemory()
			m := &canceledPreflightMemory{stepMemory: memory, address: dhcsr, extra: errMemory}
			core, err := cortexm.Acquire(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			if operation != "halt" {
				if err := core.Halt(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "step reason" {
				m.address = 0xe000ed30
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m.cancel = cancel
			writes := m.writes
			err = callPreflightOperation(ctx, core, operation)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, errMemory) || m.writes != writes {
				t.Fatalf("mixed preflight=%v", err)
			}
			if _, err := core.Halted(t.Context()); err == nil || m.writes != writes {
				t.Fatal("non-context failure left ordinary calls available")
			}
			if err := core.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
