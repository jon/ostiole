package cortexm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestHaltCancellationBeforeWritePreservesRestorationState(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable} {
		t.Run(fmt.Sprintf("debug=%d", initial), func(t *testing.T) {
			m := newControlMemory()
			m.control = initial
			core, err := cortexm.Acquire(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m.onRead = cancel
			writes := m.writes
			if err := core.Halt(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("halt error = %v", err)
			}
			if m.writes != writes {
				t.Fatal("canceled halt attempted a write")
			}
			m.onRead = nil
			if initial == debugEnable {
				m.failWrite = writes + 1
			} else {
				writes++ // Acquisition still requires restoration.
			}
			if err := core.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if m.writes != writes || m.control != initial {
				t.Fatalf("writes=%d want=%d control=%#x", m.writes, writes, m.control)
			}
		})
	}
}
