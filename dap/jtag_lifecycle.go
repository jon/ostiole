package dap

import (
	"context"
	"errors"
	"fmt"
)

func (e *jtagExecutor) configure(ctx context.Context) (uint32, error) {
	state, err := e.readDP(ctx, CTRLSTAT)
	if err != nil {
		return 0, err
	}
	if state&jtagTransactionModes != 0 {
		return 0, errors.New("dap: inherited JTAG transaction modes are active")
	}
	e.ownedOverrun = state&overrunDetect != 0
	e.faultPending = true
	if err := e.writeDP(ctx, CTRLSTAT, state&^overrunDetect|jtagSticky); err != nil {
		return 0, err
	}
	state, err = e.readDP(ctx, CTRLSTAT)
	if err != nil {
		return 0, err
	}
	if state&(overrunDetect|jtagSticky|jtagTransactionModes) != 0 {
		return 0, errors.New("dap: JTAG-DP still has sticky status, ORUNDETECT, or transaction modes set")
	}
	e.faultPending = false
	return state, nil
}

func (e *jtagExecutor) enter(ctx context.Context) error {
	e.dp.state.beginProtocolEntry()
	if err := e.chain.Connect(ctx); err != nil {
		e.dp.state.loseFraming()
		return err
	}
	tap, err := e.chain.TAP(e.index)
	if err != nil {
		e.dp.state.loseFraming()
		return err
	}
	e.tap = tap
	id, err := e.idcode(ctx)
	if err != nil {
		return err
	}
	expected := e.chain.Layout()[e.index].ResetRegister().IDCODE
	if id != expected || e.dp.reentryKnown && id != e.dp.reentryID.idcode {
		e.dp.state.loseFraming()
		return fmt.Errorf("dap: JTAG-DP IDCODE changed: got %#08x, expected %#08x", id, expected)
	}
	e.dp.reentryID, e.dp.reentryKnown = Identity{idcode: id}, true
	if err := e.abortPending(ctx); err != nil {
		return err
	}
	if err := e.prime(ctx); err != nil {
		e.dp.state.loseFraming()
		return err
	}
	if err := e.writeDP(ctx, SELECT, 0); err != nil {
		e.dp.state.loseFraming()
		return err
	}
	e.dp.state.response = responseSimple
	return nil
}

func (e *jtagExecutor) release(ctx context.Context) error {
	if err := e.repairFault(ctx); err != nil {
		return err
	}
	if e.ownedOverrun {
		state, err := e.readDP(ctx, CTRLSTAT)
		if err != nil {
			return err
		}
		if err := e.writeDP(ctx, CTRLSTAT, state&^jtagSticky|overrunDetect); err != nil {
			return err
		}
		state, err = e.readDP(ctx, CTRLSTAT)
		if err != nil {
			return err
		}
		if state&overrunDetect == 0 {
			return errors.New("dap: JTAG ORUNDETECT restoration was not accepted")
		}
		e.ownedOverrun = false
	}
	if err := e.chain.Release(ctx); err != nil {
		e.dp.state.loseFraming()
		return err
	}
	return nil
}
