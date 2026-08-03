package app

import (
	"context"
	"errors"
	"fmt"
	"io"
)

const usage = "Usage:\n  ost help\n"

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
	err := run(args, stdout)
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

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	return &usageError{message: fmt.Sprintf("unknown command %q", args[0])}
}

func isHelp(arg string) bool {
	return arg == "help" || arg == "--help" || arg == "-h"
}
