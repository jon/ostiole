package dap

import "errors"

type completedReleaseError struct{ error }

func (e completedReleaseError) Unwrap() error { return e.error }

func releaseCompletionKnown(err error) bool {
	var completed completedReleaseError
	return errors.As(err, &completed)
}
