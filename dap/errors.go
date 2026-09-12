package dap

import (
	"errors"
	"strings"

	"github.com/jon/ostiole/swd"
)

var (
	// ErrWait reports a WAIT response that could not be completed.
	ErrWait = errors.New("dap: incomplete WAIT response")
	// ErrFault reports a failed debug-port or access-port operation.
	ErrFault = errors.New("dap: debug-port fault")
	// ErrProtocol reports an invalid protocol response.
	ErrProtocol = errors.New("dap: invalid protocol response")
	// ErrResultPending reports access to a queued result before Commit.
	ErrResultPending = errors.New("dap: transaction result is pending")
	// ErrTxnCommitted reports reuse of a single-use transaction.
	ErrTxnCommitted = errors.New("dap: transaction is already committed")
	// ErrNotExecuted reports an operation not sent because validation or
	// earlier execution stopped the transaction.
	ErrNotExecuted = errors.New("dap: operation was not executed")
	// ErrIndeterminate reports an operation which might have taken effect.
	ErrIndeterminate = errors.New("dap: operation outcome is indeterminate")
)

// FaultError reports the CTRL/STAT sticky state captured after a fault.
// StateValid is false when DAP could not safely read bank-zero CTRL/STAT.
type FaultError struct {
	CTRLSTAT   uint32
	StateValid bool
	cause      error
}

func (e *FaultError) Error() string {
	if e == nil || !e.StateValid {
		return "dap: FAULT (CTRL/STAT unavailable)"
	}
	var set []string
	if e.CTRLSTAT&stickyCompare != 0 {
		set = append(set, "STICKYCMP")
	}
	if e.CTRLSTAT&stickyError != 0 {
		set = append(set, "STICKYERR")
	}
	if e.CTRLSTAT&writeDataError != 0 {
		set = append(set, "WDATAERR")
	}
	if e.CTRLSTAT&stickyOverrun != 0 {
		set = append(set, "STICKYORUN")
	}
	if len(set) == 0 {
		return "dap: FAULT (CTRL/STAT sticky bits clear)"
	}
	return "dap: FAULT (" + strings.Join(set, ", ") + ")"
}

// Is identifies a fault independently of the physical protocol.
func (e *FaultError) Is(target error) bool { return e != nil && target == ErrFault }

// Unwrap retains a physical fault response when one was received.
func (e *FaultError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type portError struct{ error }

func (e portError) Unwrap() error { return e.error }

func (e portError) Is(target error) bool {
	switch target {
	case ErrWait:
		return errors.Is(e.error, swd.ErrWait)
	case ErrFault:
		return errors.Is(e.error, swd.ErrFault)
	case ErrProtocol:
		return errors.Is(e.error, swd.ErrProtocol)
	default:
		return false
	}
}

func classifyPortError(err error) error {
	if errors.Is(err, swd.ErrWait) || errors.Is(err, swd.ErrFault) || errors.Is(err, swd.ErrProtocol) {
		return portError{err}
	}
	return err
}
