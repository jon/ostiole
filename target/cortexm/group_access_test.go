package cortexm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func TestGroupRegistersAndStepUseSelectedTarget(t *testing.T) {
	a, b := newStepMemory(), newStepMemory()
	g := acquireControlGroup(t, a, b)
	if _, err := g.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	peerReads, peerWrites := b.reads, b.writes
	want := a.registers[4]
	if value, err := g.ReadRegister(t.Context(), 1, cortexm.R4); err != nil || value != want {
		t.Fatalf("R4=%#x want=%#x error=%v", value, want, err)
	}
	if err := g.WriteRegister(t.Context(), 1, cortexm.R4, 0xfeed); err != nil || a.registers[4] != 0xfeed {
		t.Fatalf("write=%v R4=%#x", err, a.registers[4])
	}
	result := g.Results()[0]
	if result.State != cortexm.Halted || !result.HaltOwned || !result.Attempted || result.Err != nil {
		t.Fatalf("step result=%+v", result)
	}
	if b.reads != peerReads || b.writes != peerWrites || b.steps != 0 || !b.halted {
		t.Fatal("member access touched its peer")
	}
	if _, err := g.Release(t.Context()); err != nil || a.registers[4] != 0xfeed {
		t.Fatalf("release=%v R4=%#x", err, a.registers[4])
	}
}

func TestGroupStepLeavesPeerStopped(t *testing.T) {
	a, b := newStepMemory(), newStepMemory()
	g := acquireControlGroup(t, a, b)
	if _, err := g.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	pc, r0 := a.registers[15], a.registers[0]
	peerReads, peerWrites := b.reads, b.writes
	if err := g.Step(t.Context(), 1); err != nil || a.steps != 1 || a.registers[15] != pc+2 || a.registers[0] != r0+1 {
		t.Fatalf("step=%v steps=%d PC=%#x R0=%#x", err, a.steps, a.registers[15], a.registers[0])
	}
	if b.reads != peerReads || b.writes != peerWrites || b.steps != 0 || !b.halted {
		t.Fatal("step touched its peer")
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGroupMemberPreconditionsDoNotInvalidatePeers(t *testing.T) {
	a, b := newStepMemory(), newStepMemory()
	g := acquireControlGroup(t, a, b)
	reads, writes := a.reads, a.writes
	if _, err := g.ReadRegister(t.Context(), 1, 0); err == nil {
		t.Fatal("invalid register accepted")
	}
	if err := g.WriteRegister(t.Context(), 1, cortexm.PC, 1); err == nil {
		t.Fatal("invalid PC accepted")
	}
	if a.reads != reads || a.writes != writes {
		t.Fatal("invalid input reached target memory")
	}
	if err := g.Step(t.Context(), 1); err == nil || a.launches != 0 {
		t.Fatalf("unowned step=%v launches=%d", err, a.launches)
	}
	if _, err := g.Halt(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReadRegister(t.Context(), 0, cortexm.R0); err == nil {
		t.Fatal("invalid member accepted")
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGroupRegisterFailureRetainsTargetLifecycle(t *testing.T) {
	a, b := newRegisterMemory(), newRegisterMemory()
	g := acquireControlGroup(t, a, b)
	if _, err := g.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	a.failWrite, a.afterWrite = a.writes+1, true
	if _, err := g.ReadRegister(t.Context(), 1, cortexm.R4); !errors.Is(err, errMemory) {
		t.Fatal(err)
	}
	transfers := a.transfers
	if err := g.WriteRegister(t.Context(), 2, cortexm.R4, 0); err == nil {
		t.Fatal("member access remained available after uncertain selection")
	}
	if _, err := g.Release(t.Context()); err != nil || a.transfers != transfers || a.control != 0 || b.control != 0 {
		t.Fatalf("release=%v transfers=%d/%d control=%#x/%#x", err, a.transfers, transfers, a.control, b.control)
	}
}

func TestGroupStepFailureDoesNotReplayLaunch(t *testing.T) {
	a, b := newStepMemory(), newStepMemory()
	g := acquireControlGroup(t, a, b)
	if _, err := g.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	a.failWrite, a.afterWrite = a.writes+1, true
	if err := g.Step(t.Context(), 1); !errors.Is(err, errMemory) || a.launches != 1 {
		t.Fatalf("step=%v launches=%d", err, a.launches)
	}
	if _, err := g.Release(t.Context()); err == nil || a.launches != 1 || b.control != 0 || g.Results()[1].CleanupPending {
		t.Fatalf("release=%v launches=%d peer control=%#x", err, a.launches, b.control)
	}
}

func TestGroupCanceledMemberAccessHasNoTraffic(t *testing.T) {
	m := newStepMemory()
	g := acquireControlGroup(t, m)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reads, writes := m.reads, m.writes
	if _, err := g.ReadRegister(ctx, 1, cortexm.R0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := g.WriteRegister(ctx, 1, cortexm.R4, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := g.Step(ctx, 1); !errors.Is(err, context.Canceled) || m.reads != reads || m.writes != writes {
		t.Fatalf("step=%v reads/writes=%d/%d", err, m.reads, m.writes)
	}
	if _, err := g.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
