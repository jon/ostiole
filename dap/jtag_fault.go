package dap

import (
	"context"
	"errors"
)

func (e *jtagExecutor) repairFault(ctx context.Context) error {
	if !e.faultPending {
		return nil
	}
	state, err := e.readDP(ctx, CTRLSTAT)
	if err != nil {
		return err
	}
	return e.clearFault(ctx, state)
}

func (e *jtagExecutor) clearFault(ctx context.Context, state uint32) error {
	e.faultPending = true
	if err := e.writeDP(ctx, CTRLSTAT, state); err != nil {
		e.dp.state.beginRepair()
		return err
	}
	state, err := e.readDP(ctx, CTRLSTAT)
	if err != nil || state&jtagSticky != 0 {
		e.dp.state.beginRepair()
		return errors.Join(err, errors.New("dap: JTAG sticky state remains unconfirmed"))
	}
	e.faultPending = false
	return nil
}
