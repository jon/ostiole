package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	operationTimeout = 5 * time.Second
	usage            = "Usage:\n" +
		"  ost ftdi list\n" +
		"  ost swd dpidr\n" +
		"  ost dap dp id\n" +
		"  ost help\n"
)

type usageError struct {
	message string
}

func (e *usageError) Error() string {
	return e.message
}

// Run dispatches one ost invocation and returns its process exit status.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if ctx == nil {
		_, _ = fmt.Fprintln(stderr, "ost: nil context")
		return 1
	}
	operationCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	err := run(operationCtx, args, stdout, systemOperations())
	if err == nil {
		return 0
	}
	var commandErr *usageError
	if errors.As(err, &commandErr) {
		_, _ = fmt.Fprintf(stderr, "ost: %s\n\n%s", commandErr.message, usage)
		return 2
	}
	_, _ = fmt.Fprintf(stderr, "ost: %v\n", err)
	return 1
}

func run(ctx context.Context, args []string, stdout io.Writer, ops operations) error {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	if args[0] == "ftdi" {
		return runFTDI(ctx, args[1:], stdout, ops)
	}
	if args[0] == "swd" {
		return runSWD(ctx, args[1:], stdout, ops)
	}
	if args[0] == "dap" {
		return runDAP(ctx, args[1:], stdout, ops)
	}
	return &usageError{message: fmt.Sprintf("unknown command %q", args[0])}
}

func isHelp(arg string) bool {
	return arg == "help" || arg == "--help" || arg == "-h"
}
