package cortexm

import "context"

func (t *Target) readError(err error) error {
	if !onlyContextError(err) || t.registerPending || t.haltUncertain || t.resumeUncertain || t.step != stepIdle {
		t.closing = true
	}
	return err
}

func onlyContextError(err error) bool {
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		causes := wrapped.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyContextError(cause) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return onlyContextError(wrapped.Unwrap())
	default:
		return false
	}
}
