package cortexm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

type completedCanceledMemory struct {
	*controlMemory
	cancel context.CancelFunc
}

func (m *completedCanceledMemory) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	value, err := m.controlMemory.ReadWord(ctx, addr)
	if err == nil && addr == dhcsr && m.cancel != nil {
		m.cancel()
		m.cancel = nil
		return 0, ctx.Err()
	}
	return value, err
}

func TestM33CanceledCompletedReadCannotRecoverHaltOwnership(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, releaseFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("debug=%t/release=%t", enabled, releaseFirst), func(t *testing.T) {
				exerciseCanceledCompletedRead(t, enabled, releaseFirst)
			})
		}
	}
}

func exerciseCanceledCompletedRead(t *testing.T, enabled, releaseFirst bool) {
	t.Helper()
	m := &completedCanceledMemory{controlMemory: newM33Memory()}
	if enabled {
		m.control = debugEnable
	}
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.status |= 1 << 26
	ctx, cancel := context.WithCancel(t.Context())
	m.cancel = cancel
	_, err = core.Halted(ctx)
	cancel()
	if !errors.Is(err, context.Canceled) || m.status&(1<<26) != 0 {
		t.Fatalf("completed read=%v status=%#x", err, m.status)
	}
	writes := m.writes
	if releaseFirst {
		err = core.Release(t.Context())
		if (err == nil) != enabled {
			t.Fatalf("release=%v, debug enabled=%t", err, enabled)
		}
	} else {
		if halted, err := core.Halted(t.Context()); err != nil || !halted {
			t.Fatalf("fresh status=%t, %v", halted, err)
		}
		if err := core.Resume(t.Context()); err == nil {
			t.Fatal("fresh status restored a lost halt claim")
		}
	}
	if m.writes != writes || !m.halted || m.control&haltRequest == 0 {
		t.Fatal("resumed a competing halt after losing restart evidence")
	}
	m.control, m.halted = debugEnable, false
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
