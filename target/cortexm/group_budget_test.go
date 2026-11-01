package cortexm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

func TestGroupUnsettledReleaseDoesNotStarveIndependentPeer(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	g, err := cortexm.AcquireGroup(t.Context(), groupMembers(a, b))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	b.stall = true
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	results, err := g.Release(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "core 2:") {
		t.Fatalf("release=%v", err)
	}
	if results[0].CleanupPending || !results[0].Attempted || results[0].Err != nil || a.control != 0 || a.halted {
		t.Fatalf("independent peer was starved: %+v, control=%#x halted=%t", results[0], a.control, a.halted)
	}
	if !results[1].CleanupPending || !results[1].Attempted || !errors.Is(results[1].Err, context.DeadlineExceeded) {
		t.Fatalf("unsettled member=%+v", results[1])
	}
	writes := a.writes
	b.stall, b.halted = false, false
	if results, err := g.Release(t.Context()); err != nil || a.writes != writes || results[0].Attempted || !results[1].Attempted {
		t.Fatalf("retry=%v results=%+v independent writes=%d/%d", err, results, a.writes, writes)
	}
}

func TestGroupCanceledReleaseAttributesEveryUnreachedMember(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	g, err := cortexm.AcquireGroup(t.Context(), groupMembers(a, b))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results, err := g.Release(ctx)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "core 1:") || !strings.Contains(err.Error(), "core 2:") {
		t.Fatalf("unattributed release=%v", err)
	}
	for _, result := range results {
		if result.Attempted || result.State != cortexm.ExecutionUnknown || !result.CleanupPending || !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("unreached result=%+v", result)
		}
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGroupReleasedMemberHasNoHaltClaim(t *testing.T) {
	m := newControlMemory()
	g, err := cortexm.AcquireGroup(t.Context(), groupMembers(m))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Halt(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	m.control, m.halted = 0, false
	results, err := g.Release(t.Context())
	if err != nil || results[0].HaltOwned || results[0].CleanupPending || !results[0].Attempted {
		t.Fatalf("released result=%+v error=%v", results, err)
	}
	cached := g.Results()
	if cached[0].HaltOwned || cached[0].CleanupPending {
		t.Fatalf("cached released claim=%+v", cached)
	}
}

type releaseDeadlineMemory struct {
	*controlMemory
	observe bool
	budgets []time.Duration
}

func (m *releaseDeadlineMemory) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	if m.observe {
		deadline, ok := ctx.Deadline()
		if !ok {
			return 0, errors.New("release requires a deadline")
		}
		m.budgets = append(m.budgets, time.Until(deadline))
	}
	return m.controlMemory.ReadWord(ctx, addr)
}

func TestGroupReleaseWithoutDeadlineBoundsEachMember(t *testing.T) {
	a := &releaseDeadlineMemory{controlMemory: newControlMemory()}
	b := &releaseDeadlineMemory{controlMemory: newControlMemory()}
	g := acquireControlGroup(t, a, b)
	a.observe, b.observe = true, true
	if _, err := g.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*releaseDeadlineMemory{a, b} {
		if len(m.budgets) == 0 || m.budgets[0] < 4*time.Second || m.budgets[0] > 5*time.Second {
			t.Fatalf("per-member release budget=%v", m.budgets)
		}
	}
}

func TestGroupSuccessfulReleaseUsesReverseMembershipOrder(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	g := acquireControlGroup(t, a, b)
	var order []int
	a.onWrite = func() { order = append(order, 1) }
	b.onWrite = func() { order = append(order, 2) }
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != 2 || order[1] != 1 {
		t.Fatalf("release order=%v", order)
	}
}

func TestGroupClosingBlocksEveryControlPath(t *testing.T) {
	m := newStepMemory()
	g := acquireControlGroup(t, m)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := g.Release(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	reads, writes := m.reads, m.writes
	for _, call := range []func() error{
		func() error { _, err := g.Halt(t.Context(), 1); return err },
		func() error { _, err := g.Resume(t.Context(), 1); return err },
		func() error { _, err := g.Status(t.Context(), 1); return err },
		func() error { _, err := g.ReadRegister(t.Context(), 1, cortexm.R0); return err },
		func() error { return g.WriteRegister(t.Context(), 1, cortexm.R0, 42) },
		func() error { return g.Step(t.Context(), 1) },
	} {
		if err := call(); err == nil || m.reads != reads || m.writes != writes {
			t.Fatalf("closing group operation=%v reads=%d/%d writes=%d/%d", err, m.reads, reads, m.writes, writes)
		}
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGroupAcquisitionCleanupUsesFreshContextForEarlierPeer(t *testing.T) {
	a, b := newControlMemory(), newControlMemory()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.onWrite = cancel
	g, err := cortexm.AcquireGroup(ctx, groupMembers(a, b))
	if g != nil || !errors.Is(err, context.Canceled) || a.control != 0 || b.control != 0 || a.writes != 2 {
		t.Fatalf("group=%v acquisition=%v control=%#x/%#x writes=%d", g, err, a.control, b.control, a.writes)
	}
}

func TestGroupFailedStatusInvalidatesUnreachedCachedState(t *testing.T) {
	a, b, c := newControlMemory(), newControlMemory(), newControlMemory()
	g := acquireControlGroup(t, a, b, c)
	if _, err := g.Status(t.Context(), 1, 2, 3); err != nil {
		t.Fatal(err)
	}
	b.failRead = b.reads + 1
	results, err := g.Status(t.Context(), 1, 2, 3)
	if !errors.Is(err, errMemory) || results[0].State != cortexm.Running || results[1].State != cortexm.ExecutionUnknown || results[2].State != cortexm.ExecutionUnknown {
		t.Fatalf("partial status=%+v error=%v", results, err)
	}
	if !results[1].Attempted || results[2].Attempted {
		t.Fatalf("partial attempts=%+v", results)
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGroupFailedRegisterAccessInvalidatesObservedState(t *testing.T) {
	m := newStepMemory()
	g := acquireControlGroup(t, m)
	if _, err := g.Halt(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	m.failRead = m.reads + 1
	_, err := g.ReadRegister(t.Context(), 1, cortexm.R0)
	result := g.Results()[0]
	if !errors.Is(err, errMemory) || !result.Attempted || result.State != cortexm.ExecutionUnknown || !errors.Is(result.Err, errMemory) {
		t.Fatalf("failed access=%v result=%+v", err, result)
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
