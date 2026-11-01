package swd

import (
	"context"
	"errors"
	"time"
)

// Option configures a connection without traffic. New ignores zero options.
type Option struct {
	completionTimeout time.Duration
	completionSet     bool
}

// WithCompletionTimeout sets the independent completion budget for one started
// frame or packed chunk. The default is one second. Nonpositive values are
// rejected before traffic. This budget is separate from failed-Connect cleanup.
func WithCompletionTimeout(timeout time.Duration) Option {
	return Option{completionTimeout: timeout, completionSet: true}
}

func (c *Conn) completionContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := c.validateCompletion(); err != nil {
		return nil, nil, err
	}
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.completionTimeout)
	return completionCtx, cancel, nil
}

func (c *Conn) validateCompletion() error {
	if c.completionTimeout <= 0 {
		return errors.New("swd: completion timeout must be positive")
	}
	return nil
}

func completionError(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil && !errors.Is(err, cause) {
		return errors.Join(err, cause)
	}
	return err
}
