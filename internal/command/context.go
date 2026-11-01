// Package command supplies process cancellation for Ostiole executables.
package command

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// SignalContext starts listening for interrupt and termination signals. The
// returned stop function releases the handlers; call it after cleanup finishes.
func SignalContext() (ctx context.Context, stop context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
