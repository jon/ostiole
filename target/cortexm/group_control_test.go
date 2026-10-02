package cortexm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func acquireControlGroup(t *testing.T, memories ...cortexm.Memory) *cortexm.Group {
	t.Helper()
	g, err := cortexm.AcquireGroup(t.Context(), groupMembers(memories...))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGroupSelectedControlAndStatus(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	g := acquireControlGroup(t, a, b)
	results, err := g.Halt(t.Context(), 2, 1)
	if err != nil || len(results) != 2 || results[0].ID != 1 || results[1].ID != 2 {
		t.Fatalf("halt=%+v error=%v", results, err)
	}
	for _, result := range results {
		if result.State != cortexm.Halted || !result.HaltOwned || !result.Attempted || result.Skipped {
			t.Fatalf("halt=%+v", result)
		}
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGroupSelectiveResumeAndLiveStatus(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	g := acquireControlGroup(t, a, b)
	if _, err := g.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	results, err := g.Resume(t.Context(), 1)
	if err != nil || len(results) != 1 || results[0].State != cortexm.Running || results[0].HaltOwned || a.halted || !b.halted {
		t.Fatalf("resume=%+v error=%v halted=%v/%v", results, err, a.halted, b.halted)
	}
	results, err = g.Status(t.Context(), 1, 2)
	if err != nil || results[0].State != cortexm.Running || results[1].State != cortexm.Halted || !results[1].HaltOwned {
		t.Fatalf("status=%+v error=%v", results, err)
	}
	if _, err := g.Release(t.Context()); err != nil || a.control != 0 || b.control != 0 {
		t.Fatalf("release=%v control=%#x/%#x", err, a.control, b.control)
	}
	for _, result := range g.Results() {
		if result.State != cortexm.ExecutionUnknown || result.HaltOwned {
			t.Fatalf("release retained stale state=%+v", result)
		}
	}
}

func TestGroupResumeSkipsUnownedStopsAndRunningCores(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	a.control, a.halted = debugEnable|haltRequest, true
	g := acquireControlGroup(t, a, b)
	aWrites, bWrites := a.writes, b.writes
	results, err := g.Resume(t.Context(), 1, 2)
	if err != nil || !results[0].Skipped || !results[1].Skipped || results[0].State != cortexm.Halted || results[1].State != cortexm.Running {
		t.Fatalf("resume=%+v error=%v", results, err)
	}
	if a.writes != aWrites || b.writes != bWrites {
		t.Fatal("resume wrote an unowned core")
	}
	if results, err = g.Halt(t.Context(), 1); err != nil || results[0].HaltOwned {
		t.Fatalf("inherited halt=%+v error=%v", results, err)
	}
	if _, err := g.Release(t.Context()); err != nil || !a.halted {
		t.Fatalf("release=%v inherited halt=%v", err, a.halted)
	}
}

func TestGroupInvalidSelectionHasNoEffects(t *testing.T) {
	m := newControlMemory()
	g := acquireControlGroup(t, m)
	for _, call := range []func(context.Context, ...cortexm.CoreID) ([]cortexm.CoreResult, error){g.Halt, g.Resume, g.Status} {
		for _, ids := range [][]cortexm.CoreID{nil, {0}, {2}, {1, 1}, {1, 2}} {
			reads, writes := m.reads, m.writes
			if _, err := call(t.Context(), ids...); err == nil || m.reads != reads || m.writes != writes {
				t.Fatalf("selection=%v error=%v reads/writes=%d/%d", ids, err, m.reads, m.writes)
			}
		}
		if _, err := call(nil, 1); err == nil {
			t.Fatal("accepted nil context")
		}
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Halt(t.Context(), 1); err == nil {
		t.Fatal("halted a released group")
	}
	var nilGroup *cortexm.Group
	for _, inactive := range []*cortexm.Group{nilGroup, {}} {
		if _, err := inactive.Status(t.Context(), 1); err == nil {
			t.Fatal("accepted an inactive group")
		}
	}
}

func TestGroupHaltFailureRetainsPartialSuccess(t *testing.T) {
	a, b, c := newControlMemory(), newControlMemory(), newControlMemory()
	g := acquireControlGroup(t, a, b, c)
	b.failRead = b.reads + 1
	cReads := c.reads
	results, err := g.Halt(t.Context(), 3, 2, 1)
	if !errors.Is(err, errMemory) || len(results) != 3 {
		t.Fatalf("halt=%+v error=%v", results, err)
	}
	if !a.halted || !results[0].HaltOwned || results[0].State != cortexm.Halted || results[1].State != cortexm.ExecutionUnknown || !errors.Is(results[1].Err, errMemory) || results[2].Attempted || c.reads != cReads {
		t.Fatalf("partial halt=%+v", results)
	}
	if _, err := g.Resume(t.Context(), 1); err == nil {
		t.Fatal("ordinary control remained available after member failure")
	}
	if _, err := g.Release(t.Context()); err != nil || a.halted || a.control != 0 || b.control != 0 || c.control != 0 {
		t.Fatalf("cleanup=%v controls=%#x/%#x/%#x", err, a.control, b.control, c.control)
	}
}

func TestGroupUncertainHaltNeverReplays(t *testing.T) {
	for _, after := range []bool{false, true} {
		a, b := newControlMemory(), newControlMemory()
		g := acquireControlGroup(t, a, b)
		a.failWrite, a.afterWrite = a.writes+1, after
		results, err := g.Halt(t.Context(), 1, 2)
		if !errors.Is(err, errMemory) || results[0].HaltOwned || results[1].Attempted {
			t.Fatalf("after=%v results=%+v error=%v", after, results, err)
		}
		writes := a.writes
		_, err = g.Release(t.Context())
		if after {
			if err == nil || a.writes != writes || !g.Results()[0].CleanupPending {
				t.Fatalf("uncertain halt cleanup=%v writes=%d/%d", err, a.writes, writes)
			}
		} else if err != nil || a.control != 0 {
			t.Fatalf("rejected halt cleanup=%v control=%#x", err, a.control)
		}
		if b.control != 0 || g.Results()[1].CleanupPending {
			t.Fatal("uncertain member prevented independent cleanup")
		}
	}
}

func TestGroupCancellationAfterHaltRetainsPeerOutcome(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	g := acquireControlGroup(t, a, b)
	ctx, cancel := context.WithCancel(t.Context())
	a.onWrite = cancel
	results, err := g.Halt(ctx, 1, 2)
	if !errors.Is(err, context.Canceled) || !a.halted || results[1].Attempted || results[0].State != cortexm.ExecutionUnknown {
		t.Fatalf("results=%+v error=%v", results, err)
	}
	if _, err := g.Release(t.Context()); err != nil || a.halted || a.control != 0 || b.control != 0 {
		t.Fatalf("cleanup=%v controls=%#x/%#x", err, a.control, b.control)
	}
}

func TestGroupM33RestartLeavesCompetingStopUnowned(t *testing.T) {
	m := newControlMemory()
	m.cpuid, m.status = 0x411fd210, 1<<20
	g := acquireControlGroup(t, m)
	if _, err := g.Halt(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	m.status |= 1 << 26
	writes := m.writes
	results, err := g.Resume(t.Context(), 1)
	if err != nil || !results[0].Skipped || results[0].HaltOwned || m.writes != writes || !m.halted {
		t.Fatalf("resume=%+v error=%v writes=%d/%d", results, err, m.writes, writes)
	}
	if _, err := g.Release(t.Context()); err == nil || !m.halted || m.writes != writes {
		t.Fatalf("competing halt cleanup=%v", err)
	}
}

func TestGroupPartialResumeRetainsConfirmedAndUncertainEffects(t *testing.T) {
	for _, after := range []bool{false, true} {
		a, b, c := newControlMemory(), newControlMemory(), newControlMemory()
		g := acquireControlGroup(t, a, b, c)
		if _, err := g.Halt(t.Context(), 1, 2, 3); err != nil {
			t.Fatal(err)
		}
		b.failWrite, b.afterWrite = b.writes+1, after
		results, err := g.Resume(t.Context(), 1, 2, 3)
		if !errors.Is(err, errMemory) || results[0].State != cortexm.Running || results[0].HaltOwned || results[1].State != cortexm.ExecutionUnknown || results[2].Attempted || !c.halted {
			t.Fatalf("after=%v results=%+v error=%v", after, results, err)
		}
		bWrites := b.writes
		_, err = g.Release(t.Context())
		if after && err != nil {
			t.Fatal(err)
		}
		if !after && (err == nil || b.writes != bWrites) {
			t.Fatalf("replayed uncertain resume: error=%v writes=%d/%d", err, b.writes, bWrites)
		}
		if a.halted || c.halted || a.control != 0 || c.control != 0 {
			t.Fatal("confirmed members did not restore independently")
		}
	}
}

func TestGroupPermissionLossKeepsCleanupRetryable(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	a.cpuid, a.status = 0x411fd210, 1<<20
	g := acquireControlGroup(t, a, b)
	if _, err := g.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	a.status = 0
	if _, err := g.Status(t.Context(), 1, 2); err == nil {
		t.Fatal("accepted lost permission")
	}
	if results, err := g.Release(t.Context()); err == nil || !results[0].CleanupPending || results[1].CleanupPending || !a.halted {
		t.Fatalf("release=%+v error=%v", results, err)
	}
	a.status = 1 << 20
	if _, err := g.Release(t.Context()); err != nil || a.halted || a.control != 0 {
		t.Fatalf("retry=%v control=%#x", err, a.control)
	}
}
