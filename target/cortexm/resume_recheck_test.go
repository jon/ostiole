package cortexm_test

import (
	"fmt"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestM33ResumeReobservesOwnershipBeforeWrite(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("group=%t/debug=%t", group, enabled), func(t *testing.T) { exerciseM33ResumeRecheck(t, group, enabled) })
		}
	}
}

func exerciseM33ResumeRecheck(t *testing.T, group, enabled bool) {
	t.Helper()
	m := newM33Memory()
	if enabled {
		m.control = debugEnable
	}
	var resume func() error
	var release func() error
	if group {
		g := acquireControlGroup(t, m)
		if _, err := g.Halt(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
		m.onRead = func() { m.status |= 1 << 26 }
		resume = func() error { _, err := g.Resume(t.Context(), 1); return err }
		release = func() error { _, err := g.Release(t.Context()); return err }
	} else {
		core, err := cortexm.Acquire(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if err := core.Halt(t.Context()); err != nil {
			t.Fatal(err)
		}
		m.status |= 1 << 26
		resume = func() error { return core.Resume(t.Context()) }
		release = func() error { return core.Release(t.Context()) }
	}
	writes := m.writes
	if err := resume(); err == nil || m.writes != writes || !m.halted || m.control&haltRequest == 0 {
		t.Fatalf("competing resume=%v, writes=%d/%d", err, m.writes, writes)
	}
	m.onRead = nil
	err := release()
	if (err == nil) != enabled || m.writes != writes || !m.halted {
		t.Fatalf("competing release=%v, writes=%d/%d", err, m.writes, writes)
	}
	if !enabled {
		m.control, m.halted = debugEnable, false
		if err := release(); err != nil || m.control != 0 {
			t.Fatalf("confirmed running release=%v", err)
		}
	}
}
