package dap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jon/ostiole/swd"
)

const (
	maxWaitRetries      = 100
	waitRecoveryTimeout = time.Second

	dapAbort = uint32(1 << 0)

	clearStickyCompare  = uint32(1 << 1)
	clearStickyError    = uint32(1 << 2)
	clearWriteDataError = uint32(1 << 3)
	clearStickyOverrun  = uint32(1 << 4)

	stickyOverrun  = uint32(1 << 1)
	stickyCompare  = uint32(1 << 4)
	stickyError    = uint32(1 << 5)
	writeDataError = uint32(1 << 7)
)

var errFramingUnknown = errors.New("dap: SWD framing is unknown")

func (dp *DebugPort) transfer(ctx context.Context, req swd.Request, data uint32) (uint32, error) {
	return dp.transferWithAPRecovery(ctx, req, data, waitMayAffectAP(req), true)
}

func (dp *DebugPort) transferDPWriteBarrier(ctx context.Context) (uint32, error) {
	value, err := dp.transferWithAPRecovery(ctx, swd.Request{Read: true, Addr: uint8(RDBUFF)}, 0, false, false)
	if err == nil || errors.Is(err, swd.ErrParity) || faultHasValidState(err) {
		dp.state.settleDPWrite()
	}
	return value, err
}

func (dp *DebugPort) transferWithAPRecovery(ctx context.Context, req swd.Request, data uint32, apWork, settlePrevious bool) (uint32, error) {
	if dp.state.response == responseLost {
		return 0, errFramingUnknown
	}
	waits := 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, dp.stopWaiting(waits, err, apWork)
		}
		value, err := dp.conn.Transfer(ctx, req, data)
		dp.resolveSELECT(req, value, err)
		if settlePrevious && responseSettlesPreviousDPWrite(req, err) {
			dp.state.settleDPWrite()
		}
		if err == nil {
			return value, nil
		}
		if !errors.Is(err, swd.ErrWait) {
			return dp.finishRetryError(req, value, err, waits, apWork)
		}
		if err := dp.validateWait(req, err); err != nil {
			return 0, err
		}
		waits++
		if waits > maxWaitRetries {
			cause := fmt.Errorf("dap: WAIT retry limit exceeded: %w", swd.ErrWait)
			return 0, dp.finishWait(cause, apWork)
		}
	}
}

func responseSettlesPreviousDPWrite(req swd.Request, err error) bool {
	if err != nil && err != swd.ErrWait && err != swd.ErrParity {
		return false
	}
	return req.AP || req.Read && req.Addr != uint8(DPIDR)
}

func (dp *DebugPort) resolveSELECT(req swd.Request, value uint32, err error) {
	if !dp.state.selectPending {
		return
	}
	switch {
	case isABORTWrite(req):
		dp.resolveSELECTAfterABORT(err)
	case isDPIDRRead(req):
		return
	case isCTRLSTATRead(req) && dp.state.selectDP.valid && dp.state.dpBank() == 0:
		if err == nil {
			dp.state.resolveSELECTFromCTRLSTAT(value)
		}
	case err == nil || err == swd.ErrWait || err == swd.ErrParity:
		dp.state.confirmSELECT()
	}
}

func (dp *DebugPort) resolveSELECTAfterABORT(err error) {
	if err == nil {
		dp.state.invalidateSELECT()
	}
}

func isABORTWrite(req swd.Request) bool {
	return !req.AP && !req.Read && req.Addr == uint8(ABORT)
}

func isDPIDRRead(req swd.Request) bool {
	return !req.AP && req.Read && req.Addr == uint8(DPIDR)
}

func isCTRLSTATRead(req swd.Request) bool {
	return !req.AP && req.Read && req.Addr == uint8(CTRLSTAT)
}

func (dp *DebugPort) stopWaiting(waits int, cause error, apWork bool) error {
	if waits == 0 {
		return cause
	}
	return dp.finishWait(errors.Join(swd.ErrWait, cause), apWork)
}

func (dp *DebugPort) finishRetryError(req swd.Request, value uint32, err error, waits int, apWork bool) (uint32, error) {
	if err == swd.ErrFault {
		return 0, dp.handleFault(req, apWork)
	}
	if waits == 0 && err == swd.ErrParity {
		return value, err
	}
	if waits == 0 {
		return 0, dp.invalidateTransfer(err)
	}
	cause := errors.Join(swd.ErrWait, fmt.Errorf("dap: WAIT retry failed: %w", err))
	return 0, dp.invalidateWait(cause)
}

func (dp *DebugPort) handleFault(req swd.Request, apWork bool) error {
	fault := &FaultError{}
	if dp.state.response != responseSimple || !dp.state.faultBankZero() {
		dp.state.loseFraming()
		return errors.Join(fault, errors.New("dap: cannot read CTRL/STAT after FAULT without the simple response grammar and a known bank-zero selection"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitRecoveryTimeout)
	defer cancel()
	state, err := dp.conn.Transfer(ctx, swd.Request{Read: true, Addr: uint8(CTRLSTAT)}, 0)
	if err != nil {
		dp.state.loseFraming()
		return errors.Join(fault, fmt.Errorf("dap: read CTRL/STAT after FAULT: %w", err))
	}
	fault.CTRLSTAT = state
	fault.StateValid = true
	dp.state.settleDPWrite()
	dp.state.resolveSELECTFromCTRLSTAT(state)
	minimal := dp.faultIdentityMinimal(state)
	if stickyClearForState(state, minimal) != 0 {
		if err := dp.clearFaultState(ctx, fault, minimal, apWork); err != nil {
			return err
		}
	}
	if apWork {
		dp.state.invalidateAP()
	}
	return fault
}

func (dp *DebugPort) clearFaultState(ctx context.Context, fault *FaultError, minimal bool, apWork bool) error {
	clear := stickyClearForState(fault.CTRLSTAT, minimal)
	if _, err := dp.conn.Transfer(ctx, swd.Request{Addr: uint8(ABORT)}, clear); err != nil {
		dp.state.loseFraming()
		return errors.Join(fault, fmt.Errorf("dap: clear sticky state after FAULT: %w", err))
	}
	state, err := dp.conn.Transfer(ctx, swd.Request{Read: true, Addr: uint8(CTRLSTAT)}, 0)
	if err != nil {
		dp.state.loseFraming()
		return errors.Join(fault, fmt.Errorf("dap: verify sticky state after FAULT: %w", err))
	}
	if remaining := state & supportedStickyState(minimal); remaining != 0 {
		if apWork {
			dp.state.invalidateAP()
		}
		dp.state.beginRepair()
		return errors.Join(fault, fmt.Errorf("dap: sticky state remains after FAULT cleanup: CTRL/STAT=%#08x", state))
	}
	return nil
}

func (dp *DebugPort) faultIdentityMinimal(state uint32) bool {
	if dp.reentryKnown {
		return dp.reentryID.Minimal
	}
	if dp.identified {
		return dp.identity.Minimal
	}
	return state&stickyCompare == 0
}

func (dp *DebugPort) invalidateTransfer(cause error) error {
	dp.state.loseFraming()
	return fmt.Errorf("dap: SWD framing is unknown after transfer failure: %w", cause)
}

func (dp *DebugPort) validateWait(req swd.Request, err error) error {
	if dp.waitForbidden(req) {
		cause := fmt.Errorf("dap: non-stallable request returned WAIT: %w", err)
		if err != swd.ErrWait {
			return dp.invalidateWait(cause)
		}
		return cause
	}
	if err == swd.ErrWait {
		if dp.state.response == responseSimple {
			return nil
		}
		cause := fmt.Errorf("dap: cannot retry WAIT until CTRL/STAT.ORUNDETECT is confirmed clear: %w", err)
		return dp.invalidateWait(cause)
	}
	return dp.invalidateWait(fmt.Errorf("dap: complete WAIT response: %w", err))
}

func (dp *DebugPort) waitForbidden(req swd.Request) bool {
	if req.AP {
		return false
	}
	return req.Read && (req.Addr == uint8(DPIDR) ||
		(req.Addr == uint8(CTRLSTAT) && dp.state.selectDP.valid && dp.state.dpBank() == 0)) ||
		!req.Read && req.Addr == uint8(ABORT)
}

type rejectedRequestError struct {
	error
}

func (e rejectedRequestError) Unwrap() error {
	return e.error
}

func (dp *DebugPort) finishWait(cause error, apWork bool) error {
	cause = rejectedRequestError{error: cause}
	if !apWork {
		return cause
	}
	return dp.abortWait(cause)
}

func requestWasRejected(err error) bool {
	var rejected rejectedRequestError
	return errors.As(err, &rejected)
}

func (dp *DebugPort) invalidateWait(cause error) error {
	dp.state.loseFraming()
	return fmt.Errorf("dap: AP state is unknown after incomplete WAIT recovery: %w", cause)
}

func waitMayAffectAP(req swd.Request) bool {
	return req.AP || req.Read && req.Addr == uint8(RDBUFF)
}

func (dp *DebugPort) abortWait(cause error) error {
	dp.state.invalidateAP()
	ctx, cancel := context.WithTimeout(context.Background(), waitRecoveryTimeout)
	defer cancel()

	_, err := dp.conn.Transfer(ctx, swd.Request{}, dapAbort)
	if err != nil {
		dp.state.loseFraming()
		return errors.Join(cause, fmt.Errorf("dap: DAPABORT after extended WAIT: %w", err))
	}
	if err := dp.restoreAfterAbort(ctx); err != nil {
		dp.state.loseFraming()
		return errors.Join(cause, err)
	}
	return cause
}

func (dp *DebugPort) restoreAfterAbort(ctx context.Context) error {
	state, err := dp.conn.Transfer(ctx, swd.Request{Read: true, Addr: uint8(CTRLSTAT)}, 0)
	if err != nil {
		return fmt.Errorf("dap: read sticky state after DAP abort: %w", err)
	}
	dp.state.confirmResponse(state)
	if dp.state.response != responseSimple {
		return errors.New("dap: CTRL/STAT.ORUNDETECT became enabled during DAP abort recovery")
	}
	clear := stickyClearForState(state, dp.identity.Minimal)
	if clear != 0 {
		if _, err := dp.conn.Transfer(ctx, swd.Request{}, clear); err != nil {
			return fmt.Errorf("dap: clear sticky state after DAP abort: %w", err)
		}
	}
	if err := dp.writeDP(ctx, SELECT, 0); err != nil {
		return fmt.Errorf("dap: restore SELECT after DAP abort: %w", err)
	}
	return nil
}

func supportedStickyClear(minimal bool) uint32 {
	clear := clearStickyError | clearWriteDataError | clearStickyOverrun
	if !minimal {
		clear |= clearStickyCompare
	}
	return clear
}

func supportedStickyState(minimal bool) uint32 {
	state := stickyError | writeDataError | stickyOverrun
	if !minimal {
		state |= stickyCompare
	}
	return state
}

func stickyClearForState(state uint32, minimal bool) uint32 {
	var clear uint32
	if state&stickyError != 0 {
		clear |= clearStickyError
	}
	if state&writeDataError != 0 {
		clear |= clearWriteDataError
	}
	if state&stickyOverrun != 0 {
		clear |= clearStickyOverrun
	}
	if !minimal && state&stickyCompare != 0 {
		clear |= clearStickyCompare
	}
	return clear
}
