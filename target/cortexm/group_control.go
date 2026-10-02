package cortexm

import (
	"context"
	"errors"
)

// ExecutionState is the execution state observed by a group operation.
// It describes an observation, not a simultaneous snapshot of its members.
type ExecutionState uint8

const (
	// ExecutionUnknown means the operation did not establish execution state.
	ExecutionUnknown ExecutionState = iota
	// Running means the processor was observed outside Debug state.
	Running
	// Halted means the processor was observed in Debug state.
	Halted
)

// Halt requests and confirms selected stops in membership order. An existing
// stop remains unowned. Empty, duplicate, and unknown selections fail before
// traffic. Failure stops the operation without resuming earlier successes;
// results include selected members not reached. An uncertain member leaves
// the entire group available only for Release and cached Results.
func (g *Group) Halt(ctx context.Context, ids ...CoreID) ([]CoreResult, error) {
	return g.control(ctx, ids, haltCore)
}

// Resume observes selected cores, then resumes only owned halt requests.
// Running or unowned cores are reported as Skipped without a control write.
// Failure retains partial progress and never replays an uncertain resume.
// Selection and failure rules are those of Halt. Execution is not undone.
func (g *Group) Resume(ctx context.Context, ids ...CoreID) ([]CoreResult, error) {
	return g.control(ctx, ids, resumeCore)
}

// Status observes selected cores in membership order without claiming stops.
// Like Target.Halted, it consumes sticky DHCSR status and can relinquish a halt
// claim on M33 restart. Selection and failure rules are those of Halt.
func (g *Group) Status(ctx context.Context, ids ...CoreID) ([]CoreResult, error) {
	return g.control(ctx, ids, statusCore)
}

type coreControl func(context.Context, *Target) (ExecutionState, bool, error)

func (g *Group) control(ctx context.Context, ids []CoreID, call coreControl) ([]CoreResult, error) {
	selected, err := g.selectCores(ids)
	if err != nil {
		return nil, err
	}
	for _, i := range selected {
		core := &g.cores[i]
		core.result.Attempted, core.result.Skipped = false, false
		core.result.State, core.result.Err = ExecutionUnknown, nil
	}
	for _, i := range selected {
		core := &g.cores[i]
		if err = liveContext(ctx); err != nil {
			core.result.Err = err
			break
		}
		core.result.Attempted = true
		core.result.State, core.result.Skipped, err = call(ctx, core.target)
		core.result.Err = err
		if err != nil {
			g.closing = core.target.closing
			break
		}
	}
	results := g.selectedResults(selected)
	for _, result := range results {
		if result.Err != nil {
			return results, coreError(result.ID, result.Err)
		}
	}
	return results, nil
}

func (g *Group) selectCores(ids []CoreID) ([]int, error) {
	if g == nil || len(g.cores) == 0 || g.closing {
		return nil, errors.New("cortexm: group is unavailable; release may be pending")
	}
	if len(ids) == 0 {
		return nil, errors.New("cortexm: empty core selection")
	}
	wanted := make(map[CoreID]bool, len(ids))
	for _, id := range ids {
		if id == 0 || wanted[id] {
			return nil, errors.New("cortexm: require unique nonzero selected core IDs")
		}
		wanted[id] = true
	}
	var selected []int
	for i, core := range g.cores {
		if wanted[core.result.ID] {
			selected = append(selected, i)
		}
	}
	if len(selected) != len(ids) {
		return nil, errors.New("cortexm: selected core is not a group member")
	}
	return selected, nil
}

func (g *Group) selectedResults(selected []int) []CoreResult {
	all := g.Results()
	results := make([]CoreResult, len(selected))
	for i, index := range selected {
		results[i] = all[index]
	}
	return results
}

func haltCore(ctx context.Context, t *Target) (ExecutionState, bool, error) {
	if err := t.Halt(ctx); err != nil {
		return ExecutionUnknown, false, err
	}
	return Halted, false, nil
}

func resumeCore(ctx context.Context, t *Target) (ExecutionState, bool, error) {
	state, _, err := statusCore(ctx, t)
	if err != nil || !t.haltOwned {
		return state, err == nil, err
	}
	if err := t.Resume(ctx); err != nil {
		return ExecutionUnknown, false, err
	}
	return Running, false, nil
}

func statusCore(ctx context.Context, t *Target) (ExecutionState, bool, error) {
	halted, err := t.Halted(ctx)
	if err != nil {
		return ExecutionUnknown, false, err
	}
	if halted {
		return Halted, false, nil
	}
	return Running, false, nil
}
