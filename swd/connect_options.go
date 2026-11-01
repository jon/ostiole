package swd

import "time"

type connectOptions struct {
	cleanupTimeout time.Duration
}

// ConnectOption configures one Connect attempt.
type ConnectOption func(*connectOptions)

// WithCleanupTimeout sets the independent budget for restoring acquired state
// after Connect fails. The default is one second. A nonpositive timeout rejects
// Connect before wire traffic. It does not change the supplied operation context
// or the contexts used by explicit Release calls.
func WithCleanupTimeout(timeout time.Duration) ConnectOption {
	return func(options *connectOptions) {
		options.cleanupTimeout = timeout
	}
}
