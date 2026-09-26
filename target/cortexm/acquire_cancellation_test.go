package cortexm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

type cancelReadMemory struct {
	*controlMemory
	cancel context.CancelFunc
}

func (m cancelReadMemory) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	value, err := m.controlMemory.ReadWord(ctx, addr)
	if addr == dhcsr {
		m.cancel()
	}
	return value, err
}

func TestAcquireCancellationAfterReadNeedsNoRestoration(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := newControlMemory()
	m.failWrite = 1
	core, err := cortexm.Acquire(ctx, cancelReadMemory{m, cancel})
	if core != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("core=%v err=%v", core, err)
	}
	if m.reads != 2 || m.writes != 0 || m.control != 0 {
		t.Fatalf("reads=%d writes=%d control=%#x", m.reads, m.writes, m.control)
	}
}
