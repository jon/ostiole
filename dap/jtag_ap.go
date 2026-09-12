package dap

import (
	"context"
	"errors"
	"fmt"
)

func (e *jtagExecutor) accessAP(ctx context.Context, req transferRequest, data uint32) transferResult {
	if err := e.repairFault(ctx); err != nil {
		return transferResult{cause: err, outcome: transferUnsent}
	}
	result := e.transfer(ctx, req, data)
	if result.cause != nil {
		if result.outcome == transferInFlight || result.outcome == transferIndeterminate {
			result.cause = errors.Join(result.cause, e.abortAP())
		}
		return result
	}
	state, err := e.readDP(ctx, CTRLSTAT)
	if err != nil {
		result.outcome = transferIndeterminate
		result.cause = errors.Join(err, e.abortAP())
		return result
	}
	if state&jtagSticky != 0 {
		if !req.Read {
			result.outcome = transferIndeterminate
		}
		cleanupCtx, cancel := e.dp.cleanupContext()
		defer cancel()
		result.cause = errors.Join(&FaultError{CTRLSTAT: state, StateValid: true}, e.clearFault(cleanupCtx, state))
	}
	return result
}

func (e *jtagExecutor) abortAP() error {
	e.dp.state.invalidateAP()
	e.abortRequired = true
	e.faultPending = true
	ctx, cancel := e.dp.cleanupContext()
	defer cancel()
	var err error
	if !e.dp.state.responseKnown() {
		err = e.enter(ctx)
	} else {
		err = e.abortPending(ctx)
		if err == nil {
			err = e.prime(ctx)
		}
	}
	if err == nil {
		err = e.repairFault(ctx)
	}
	if err != nil {
		e.dp.state.loseFraming()
		return fmt.Errorf("dap: abort pending JTAG AP operation: %w", err)
	}
	return nil
}
