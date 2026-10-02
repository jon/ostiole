package cortexm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func groupMembers(memories ...cortexm.Memory) []cortexm.Member {
	members := make([]cortexm.Member, len(memories))
	for i, memory := range memories {
		members[i] = cortexm.Member{ID: cortexm.CoreID(i + 1), Memory: memory}
	}
	return members
}

func TestGroupAcquireAndRelease(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	b.control, b.halted = debugEnable|haltRequest, true
	members := groupMembers(a, b)
	g, err := cortexm.AcquireGroup(t.Context(), members)
	if err != nil || g == nil {
		t.Fatalf("group=%v error=%v", g, err)
	}
	members[0].ID = 99
	results := g.Results()
	if len(results) != 2 || results[0].ID != 1 || results[1].ID != 2 {
		t.Fatalf("results=%+v", results)
	}
	for _, result := range results {
		if !result.Attempted || !result.CleanupPending || result.Identity.Raw != a.cpuid || result.Err != nil {
			t.Fatalf("result=%+v", result)
		}
	}
	results[0].ID = 99
	if g.Results()[0].ID != 1 {
		t.Fatal("results expose owned membership")
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGroupReleasePreservesInheritedState(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	b.control, b.halted = debugEnable|haltRequest, true
	g, err := cortexm.AcquireGroup(t.Context(), groupMembers(a, b))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Release(t.Context()); err != nil || a.control != 0 || !b.halted || b.control != 3 {
		t.Fatalf("release=%v control=%#x/%#x halted=%v", err, a.control, b.control, b.halted)
	}
	writes := a.writes + b.writes
	if _, err := g.Release(t.Context()); err != nil || a.writes+b.writes != writes {
		t.Fatalf("repeated release=%v", err)
	}
	for _, result := range g.Results() {
		if result.CleanupPending {
			t.Fatalf("released result=%+v", result)
		}
	}
}

func TestGroupRejectsMembershipBeforeTraffic(t *testing.T) {
	m := newControlMemory()
	for _, members := range [][]cortexm.Member{
		nil, {{ID: 0, Memory: m}}, {{ID: 1}},
		{{ID: 1, Memory: m}, {ID: 1, Memory: m}},
	} {
		if g, err := cortexm.AcquireGroup(t.Context(), members); g != nil || err == nil {
			t.Fatalf("members=%v group=%v error=%v", members, g, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cortexm.AcquireGroup(ctx, groupMembers(m)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var nilContext context.Context
	if _, err := cortexm.AcquireGroup(nilContext, groupMembers(m)); err == nil {
		t.Fatal("accepted nil context")
	}
	if m.reads != 0 || m.writes != 0 {
		t.Fatal("invalid acquisition reached memory")
	}
}

func TestGroupAcquireFailureRestoresEarlierMembers(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	b.cpuid = 0
	if g, err := cortexm.AcquireGroup(t.Context(), groupMembers(a, b)); g != nil || err == nil || a.control != 0 {
		t.Fatalf("group=%v error=%v control=%#x", g, err, a.control)
	}
}

func TestGroupCopiesCompleteMembershipBeforeTraffic(t *testing.T) {
	a, b, replacement := newControlMemory(), newControlMemory(), newControlMemory()
	members := groupMembers(a, b)
	a.onWrite = func() { members[1] = cortexm.Member{ID: 9, Memory: replacement} }
	g, err := cortexm.AcquireGroup(t.Context(), members)
	if err != nil {
		t.Fatal(err)
	}
	if b.control != debugEnable || replacement.reads != 0 || g.Results()[1].ID != 2 {
		t.Fatalf("selected control=%#x replacement reads=%d results=%+v", b.control, replacement.reads, g.Results())
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGroupAcquireFailureRetainsCleanup(t *testing.T) {
	a, b, c := newControlMemory(), newControlMemory(), newControlMemory()
	a.failWrite = 2
	b.failRead, b.failWrite = 3, 2
	g, err := cortexm.AcquireGroup(t.Context(), groupMembers(a, b, c))
	if g == nil || !errors.Is(err, errMemory) {
		t.Fatalf("group=%v error=%v", g, err)
	}
	results := g.Results()
	if !results[0].CleanupPending || results[1].CleanupPending || results[2].Attempted {
		t.Fatalf("results=%+v", results)
	}
	if c.reads != 0 || c.writes != 0 {
		t.Fatal("acquired a later member after failure")
	}
	if _, err := g.Release(t.Context()); err != nil || a.control != 0 || b.control != 0 {
		t.Fatalf("release=%v control=%#x/%#x", err, a.control, b.control)
	}
}

func TestGroupReleaseAttemptsIndependentMembersAndRetries(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	g, err := cortexm.AcquireGroup(t.Context(), groupMembers(a, b))
	if err != nil {
		t.Fatal(err)
	}
	var order []int
	a.onWrite = func() { order = append(order, 1) }
	b.onWrite = func() { order = append(order, 2) }
	b.failRead = b.reads + 1
	results, err := g.Release(t.Context())
	if !errors.Is(err, errMemory) || a.control != 0 || results[0].CleanupPending || !results[1].CleanupPending {
		t.Fatalf("results=%+v error=%v", results, err)
	}
	aWrites := a.writes
	if _, err := g.Release(t.Context()); err != nil || a.writes != aWrites || b.control != 0 {
		t.Fatalf("retry=%v writes=%d/%d control=%#x", err, a.writes, aWrites, b.control)
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("cleanup order=%v", order)
	}
}

func TestGroupAcquisitionCleanupOutlivesCancellation(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	ctx, cancel := context.WithCancel(t.Context())
	a.onWrite = cancel
	g, err := cortexm.AcquireGroup(ctx, groupMembers(a, b))
	if g != nil || !errors.Is(err, context.Canceled) || a.control != 0 || b.reads != 0 {
		t.Fatalf("group=%v error=%v control=%#x peer reads=%d", g, err, a.control, b.reads)
	}
}

func TestGroupCanceledReleaseAndInactiveValues(t *testing.T) {
	var nilGroup *cortexm.Group
	var nilContext context.Context
	for _, g := range []*cortexm.Group{nilGroup, {}} {
		if results, err := g.Release(nilContext); err != nil || len(results) != 0 || len(g.Results()) != 0 {
			t.Fatalf("inactive results=%v error=%v", results, err)
		}
	}
	m := newControlMemory()
	g, err := cortexm.AcquireGroup(t.Context(), groupMembers(m))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results, err := g.Release(ctx)
	if !errors.Is(err, context.Canceled) || !results[0].CleanupPending || results[0].Attempted {
		t.Fatalf("results=%+v error=%v", results, err)
	}
	if _, err := g.Release(t.Context()); err != nil || m.control != 0 {
		t.Fatalf("retry=%v control=%#x", err, m.control)
	}
}
