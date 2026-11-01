package jtag

import "time"

// Option configures a connection without traffic. New ignores zero options.
type Option struct {
	completionTimeout time.Duration
	completionSet     bool
}

// WithCompletionTimeout sets the independent budget for a started ScanIR or
// ScanDR, including its entry, shift, and exit phases. The default is thirty
// seconds. Nonpositive durations are rejected before traffic.
func WithCompletionTimeout(timeout time.Duration) Option {
	return Option{completionTimeout: timeout, completionSet: true}
}
