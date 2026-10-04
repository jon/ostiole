package sim

import (
	"context"
	"errors"
	"fmt"
)

// ErrDeviceFailure reports a host or model failure inside a memory callback.
// The accepted access might have taken effect. The error text retains the
// callback diagnostic; only context cancellation/deadline identity is retained.
// Callback errors never identify SWD acknowledgements or unsent requests.
var ErrDeviceFailure = errors.New("dap/sim: memory device failure")

type deviceFailure struct {
	cause error
}

func (e deviceFailure) Error() string {
	return fmt.Sprintf("%v: %v", ErrDeviceFailure, e.cause)
}

// Is preserves cancellation without exposing protocol classifications from an
// accepted device access. Unwrapping the cause would let SWD treat a callback
// WAIT as permission to retry the accepted access.
func (e deviceFailure) Is(target error) bool {
	if target == ErrDeviceFailure {
		return true
	}
	return (target == context.Canceled || target == context.DeadlineExceeded) && errors.Is(e.cause, target)
}

func deviceResult(err error) error {
	if err == nil || errors.Is(err, ErrBusFault) {
		return err
	}
	return deviceFailure{cause: err}
}
