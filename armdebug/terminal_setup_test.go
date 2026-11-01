package armdebug_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/probe"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestTerminalSetupPreservesOperationCleanupAndHostErrors(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "released", true: "pending"}[pending], func(t *testing.T) {
			b := &terminalBench{bench: newBench(), possible: true, pending: true, retainHost: pending}
			operationErr := errors.New("power request failed")
			restorationErr := errors.New("cleanup lost transport")
			hostErr := errors.New("host close failed")
			b.closeErr = hostErr
			b.target.beforeWrite = func(req swdsim.Request, _ uint32, value uint32) error {
				if !req.AP && req.Addr == 4 && value&(1<<28) != 0 {
					b.wireErr = restorationErr
					b.possible = false
					return operationErr
				}
				return nil
			}
			c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
			if !errors.Is(err, operationErr) || !errors.Is(err, restorationErr) || !errors.Is(err, hostErr) || !errors.Is(err, armdebug.ErrRestorationAbandoned) {
				t.Fatalf("lost setup errors: %v", err)
			}
			if (c != nil) != pending || b.closes != 1 {
				t.Fatalf("cleanup owner %v, pending %v, closes %d", c, pending, b.closes)
			}
			before := b.transfers
			b.closeErr = nil
			b.retainHost = false
			if err := c.Close(); err != nil || b.transfers != before {
				t.Fatalf("host retry: %v", err)
			}
		})
	}
}
