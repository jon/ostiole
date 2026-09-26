package cortexm_test

import (
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestUncertainHaltDoesNotAcquireAnIndependentStop(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable} {
		m := newControlMemory()
		m.control = initial
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		m.failWrite = m.writes + 1
		if err := core.Halt(t.Context()); err == nil {
			t.Fatal("halt succeeded")
		}
		m.control, m.halted = debugEnable|haltRequest, true
		writes := m.writes
		if err := core.Release(t.Context()); err == nil || m.writes != writes || !m.halted {
			t.Fatal("cleanup resumed an unattributed halt")
		}
	}
}
