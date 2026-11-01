package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

func (e *swdExecutor) registerOperation(ctx context.Context, req transferRequest, data uint32) (uint32, RegisterStatus, error) {
	switch {
	case req.AP && req.Read:
		return e.register.ReadAP(ctx, req.Addr)
	case req.Read:
		return e.register.ReadDP(ctx, req.Addr)
	case req.AP:
		status, err := e.register.WriteAP(ctx, req.Addr, data)
		return 0, status, err
	default:
		status, err := e.register.WriteDP(ctx, req.Addr, data)
		return 0, status, err
	}
}

func (e *swdExecutor) registerExchange(ctx context.Context, req transferRequest, data uint32) transferResult {
	if err := ctx.Err(); err != nil {
		return transferResult{cause: err, outcome: transferUnsent}
	}
	value, status, err := e.registerOperation(ctx, req, data)
	err = classifyPortError(err)
	outcome := transferIndeterminate
	switch status {
	case RegisterCompleted:
		outcome = transferConfirmed
	case RegisterUnsent:
		outcome = transferUnsent
	case RegisterRejected:
		outcome = transferRejected
	case RegisterIndeterminate:
	default:
		err = errors.Join(err, ErrProtocol, fmt.Errorf("dap: invalid register status %d", status))
	}
	if !registerStatusConsistent(status, err) {
		outcome = transferIndeterminate
		err = errors.Join(err, ErrProtocol, errors.New("dap: contradictory register status"))
	}
	return transferResult{data: value, cause: err, outcome: outcome}
}

type completedRegisterError struct{ error }

func (e completedRegisterError) Unwrap() error { return e.error }
func registerCompletionKnown(err error) bool {
	var completed completedRegisterError
	return !requestWasNotSent(err) && errors.As(err, &completed)
}

func (e *swdExecutor) transferRegister(ctx context.Context, req transferRequest, data uint32, preserveAP bool) (uint32, error) {
	if e.dp.state.response == responseLost {
		return 0, errFramingUnknown
	}
	for waits := uint(0); ; {
		result := e.registerExchange(ctx, req, data)
		if result.cause == nil {
			return result.data, nil
		}
		if result.outcome == transferRejected && errors.Is(result.cause, ErrWait) {
			waits++
			if err := e.registerWait(ctx, req, result.cause, waits); err != nil {
				return 0, err
			}
			continue
		}
		return e.finishRegister(req, result, preserveAP)
	}
}

func (e *swdExecutor) registerWait(ctx context.Context, req transferRequest, cause error, waits uint) error {
	if e.waitForbidden(req) {
		e.dp.state.loseFraming()
		return errors.Join(cause, ErrProtocol, errors.New("dap: non-stallable register request returned WAIT"))
	}
	cause = canceledWAITCause(ctx, cause)
	if !soleRegisterWAIT(cause) {
		return e.finishWait(cause)
	}
	if e.dp.maxWaits != 0 && waits >= e.dp.maxWaits {
		return e.finishWait(cause)
	}
	return nil
}

func (e *swdExecutor) finishRegister(req transferRequest, result transferResult, preserveAP bool) (uint32, error) {
	if errors.Is(result.cause, ErrProtocol) {
		e.dp.state.loseFraming()
		return 0, errors.Join(result.cause, ErrIndeterminate)
	}
	if result.outcome == transferUnsent {
		return 0, &requestNotSentError{cause: errors.Join(ErrNotExecuted, result.cause)}
	}
	if errors.Is(result.cause, ErrFault) {
		return e.finishRegisterFault(req, result, preserveAP)
	}
	if result.outcome == transferConfirmed {
		if req.Read && errors.Is(result.cause, swd.ErrParity) {
			return 0, errors.Join(result.cause, ErrIndeterminate)
		}
		return result.data, completedRegisterError{error: result.cause}
	}
	if result.outcome == transferRejected {
		return 0, rejectedRequestError{error: result.cause}
	}
	e.dp.state.loseFraming()
	return 0, errors.Join(result.cause, ErrIndeterminate)
}

func registerStatusConsistent(status RegisterStatus, err error) bool {
	if err == nil {
		return status == RegisterCompleted
	}
	rejectedOrUnknown := status == RegisterRejected || status == RegisterIndeterminate
	completedOrUnknown := status == RegisterCompleted || status == RegisterIndeterminate
	claims := []struct{ present, allowed bool }{
		{errors.Is(err, ErrIndeterminate) || errors.Is(err, swd.ErrIndeterminate), status == RegisterIndeterminate},
		{errors.Is(err, ErrNotExecuted) || errors.Is(err, swd.ErrNotExecuted), status == RegisterUnsent},
		{errors.Is(err, ErrWait), rejectedOrUnknown},
		{errors.Is(err, ErrFault), status != RegisterUnsent},
		{errors.Is(err, swd.ErrParity), completedOrUnknown},
	}
	for _, claim := range claims {
		if claim.present && !claim.allowed {
			return false
		}
	}
	return true
}

func (e *swdExecutor) finishRegisterFault(req transferRequest, result transferResult, preserveAP bool) (uint32, error) {
	if req == dpTransferRequest(SELECT, false) && result.outcome != transferRejected {
		e.dp.state.invalidateSELECT()
	}
	err := errors.Join(result.cause, e.handleFault(req, waitMayAffectAP(req), preserveAP))
	if result.outcome == transferRejected {
		return 0, rejectedRequestError{error: err}
	}
	if result.outcome == transferIndeterminate || !req.Read {
		err = errors.Join(err, ErrIndeterminate)
	}
	return 0, err
}

func soleRegisterWAIT(err error) bool {
	if err == ErrWait || err == swd.ErrWait {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !soleRegisterWAIT(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return soleRegisterWAIT(cause)
	}
	return false
}
