package swd_test

import (
	"context"
	"errors"
	"testing"
	"time"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestConnectCleanupTimeoutValidationPreservesAcquisition(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	wire := &entryFailureWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	for _, timeout := range []time.Duration{0, -time.Second} {
		if _, err := conn.Connect(t.Context(), swd.WithCleanupTimeout(timeout)); err == nil {
			t.Fatal("accepted invalid budget")
		}
	}
	if before != wire.calls {
		t.Fatal("invalid budget sent traffic")
	}
	if _, err := conn.ReadDP(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("invalid budget lost acquired state")
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.OverrunDetectEnabled() {
		t.Fatal("lost inherited ORUNDETECT snapshot")
	}
}

type connectCleanupWire struct {
	swd.Wire
	fail   bool
	budget time.Duration
}

func (w *connectCleanupWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if w.fail {
		w.fail = false
		return nil, errors.New("injected reentry failure")
	}
	if bits == 136 {
		if deadline, ok := ctx.Deadline(); ok {
			w.budget = time.Until(deadline)
		}
	}
	return w.Wire.SWDIO(ctx, direction, output, bits)
}

func TestConnectCleanupBudgetIsPerAttempt(t *testing.T) {
	wire := &connectCleanupWire{Wire: swdsim.New(dapsim.New(0x2ba01477))}
	conn := swd.New(wire)
	for _, budget := range []time.Duration{3 * time.Second, time.Second} {
		if _, err := conn.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		wire.fail = true
		var options []swd.ConnectOption
		if budget != time.Second {
			options = append(options, swd.WithCleanupTimeout(budget))
		}
		if _, err := conn.Connect(t.Context(), options...); err == nil {
			t.Fatal("missing reentry failure")
		}
		if wire.budget < budget-250*time.Millisecond || wire.budget > budget {
			t.Fatalf("cleanup budget = %v; want %v", wire.budget, budget)
		}
		if err := conn.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
