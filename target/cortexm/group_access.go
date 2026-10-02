package cortexm

import "context"

// ReadRegister reads a selected member through its owned Target. The halted
// register and cleanup rules of Target.ReadRegister apply. Results retains the
// member outcome; an error returns no valid register value.
func (g *Group) ReadRegister(ctx context.Context, id CoreID, reg Register) (uint32, error) {
	var value uint32
	err := g.access(ctx, id, func(t *Target) error {
		var err error
		value, err = t.ReadRegister(ctx, reg)
		return err
	})
	if err != nil {
		return 0, err
	}
	return value, nil
}

// WriteRegister changes a selected member through its owned Target. The value
// validation, effects, and cleanup rules of Target.WriteRegister apply. Release
// does not undo register writes. Results retains the member outcome.
func (g *Group) WriteRegister(ctx context.Context, id CoreID, reg Register, value uint32) error {
	return g.access(ctx, id, func(t *Target) error { return t.WriteRegister(ctx, reg, value) })
}

// Step steps a selected member from an owned halt, preserving Target.Step's
// event and cleanup rules. It neither steps nor resumes peers. Results retains
// the member outcome. This group does not own cross-trigger routing.
func (g *Group) Step(ctx context.Context, id CoreID) error {
	return g.access(ctx, id, func(t *Target) error { return t.Step(ctx) })
}

func (g *Group) access(ctx context.Context, id CoreID, call func(*Target) error) error {
	_, err := g.control(ctx, []CoreID{id}, func(_ context.Context, t *Target) (ExecutionState, bool, error) {
		if err := call(t); err != nil {
			return ExecutionUnknown, false, err
		}
		return Halted, false, nil
	})
	return err
}
