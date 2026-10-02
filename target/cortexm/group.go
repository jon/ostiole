package cortexm

import (
	"context"
	"errors"
	"fmt"
)

// CoreID names a processor within one Group. Zero is invalid; an ID is not a
// CPUID or an access-port address.
type CoreID uint32

// Member selects one distinct physical processor through borrowed memory.
// IDs must be nonzero and unique. The caller supplies distinct processors and
// keeps exclusive control of their debug registers until group release.
type Member struct {
	ID     CoreID
	Memory Memory
}

// CoreResult is a detached member outcome. Attempted means the member operation
// was called; Skipped means Resume observed no owned halt to release. State is
// unknown unless this operation observed execution. HaltOwned is the target's
// retained halt-request claim, not proof that it stopped. CleanupPending requires
// retaining memory and lower owners. Identity survives successful release.
type CoreResult struct {
	ID             CoreID
	Identity       Identity
	Attempted      bool
	Skipped        bool
	State          ExecutionState
	HaltOwned      bool
	CleanupPending bool
	Err            error
}

type groupCore struct {
	result CoreResult
	target *Target
}

// Group owns its member targets, borrowing their memory. Do not copy it.
// Serialize calls and all access over the shared memory connection. The group
// does not close memory or its lower owners. Its zero value is inactive.
type Group struct {
	cores   []groupCore
	closing bool
}

// AcquireGroup validates and copies membership, then acquires targets in that
// order without requesting halts. Target acquisition effects and restrictions
// apply to every member. Failure stops acquisition and attempts release with a
// fresh five-second context. A non-nil group returned with an error retains
// cleanup; only Release is available. Memory remains borrowed on every return.
func AcquireGroup(ctx context.Context, members []Member) (*Group, error) {
	if err := validateMembers(members); err != nil {
		return nil, err
	}
	if err := liveContext(ctx); err != nil {
		return nil, err
	}
	members = append([]Member(nil), members...)
	g := &Group{cores: make([]groupCore, len(members))}
	for i, member := range members {
		g.cores[i].result.ID = member.ID
	}
	for i, member := range members {
		core := &g.cores[i]
		core.result.Attempted = true
		var err error
		core.target, err = Acquire(ctx, member.Memory)
		core.result.Identity = core.target.Identity()
		core.result.CleanupPending = core.target != nil
		core.result.Err = err
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), acquireCleanupTimeout)
			_, releaseErr := g.release(cleanup, true)
			cancel()
			if g.pending() {
				return g, errors.Join(coreError(member.ID, err), releaseErr)
			}
			return nil, errors.Join(coreError(member.ID, err), releaseErr)
		}
	}
	return g, nil
}

func validateMembers(members []Member) error {
	if len(members) == 0 {
		return errors.New("cortexm: empty group membership")
	}
	seen := make(map[CoreID]bool, len(members))
	for _, member := range members {
		if member.ID == 0 || seen[member.ID] || member.Memory == nil {
			return errors.New("cortexm: require unique nonzero core IDs and non-nil memory")
		}
		seen[member.ID] = true
	}
	return nil
}

// Results copies each member's most recent outcome in membership order without
// traffic; members may have been selected by different calls. Acquisition failure
// includes any cleanup errors. Nil groups return no results. Changing the
// returned slice does not change group state.
func (g *Group) Results() []CoreResult {
	if g == nil {
		return nil
	}
	results := make([]CoreResult, len(g.cores))
	for i, core := range g.cores {
		results[i] = core.result
		results[i].HaltOwned = core.target != nil && core.target.haltOwned
	}
	return results
}

// Release restores each pending target in reverse membership order and attempts
// independent members even after a failure, while ctx permits. It retains
// failures for retry and never repeats successful release. Keep lower owners
// live until every CleanupPending is false. Use a fresh bounded context after
// cancellation. Nil and inactive groups require no cleanup. Once release begins,
// only Release and cached Results remain available.
func (g *Group) Release(ctx context.Context) ([]CoreResult, error) {
	return g.release(ctx, false)
}

func (g *Group) release(ctx context.Context, preserve bool) ([]CoreResult, error) {
	if g == nil {
		return nil, nil
	}
	g.closing = true
	if !preserve {
		for i := range g.cores {
			g.cores[i].result.Attempted = false
			g.cores[i].result.Skipped = false
			g.cores[i].result.State = ExecutionUnknown
			g.cores[i].result.Err = nil
		}
	}
	if !g.pending() {
		return g.Results(), nil
	}
	var errs []error
	for i := len(g.cores) - 1; i >= 0; i-- {
		core := &g.cores[i]
		if !core.result.CleanupPending {
			continue
		}
		if err := liveContext(ctx); err != nil {
			core.result.Err = errors.Join(core.result.Err, err)
			errs = append(errs, err)
			break
		}
		core.result.Attempted = true
		err := core.target.Release(ctx)
		core.result.Err = errors.Join(core.result.Err, err)
		core.result.CleanupPending = err != nil
		if err != nil {
			errs = append(errs, coreError(core.result.ID, err))
		}
	}
	return g.Results(), errors.Join(errs...)
}

func (g *Group) pending() bool {
	for _, core := range g.cores {
		if core.result.CleanupPending {
			return true
		}
	}
	return false
}

func coreError(id CoreID, err error) error {
	return fmt.Errorf("cortexm: core %d: %w", id, err)
}
