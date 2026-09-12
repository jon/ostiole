package dap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type cleanupBudgetWire struct {
	inner       swd.Wire
	fail        bool
	calls       int
	entryBudget time.Duration
}

func (w *cleanupBudgetWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	if w.fail {
		w.fail = false
		return nil, errors.New("injected scan failure")
	}
	if bits == 136 {
		deadline, ok := ctx.Deadline()
		if ok {
			w.entryBudget = time.Until(deadline)
		}
	}
	return w.inner.SWDIO(ctx, direction, output, bits)
}

func TestCleanupTimeoutValidationIsInert(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		wire := &cleanupBudgetWire{inner: swdsim.New(sim.New(0x2ba01477))}
		dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)), dap.WithCleanupTimeout(timeout))
		if _, err := dp.Connect(t.Context()); err == nil || wire.calls != 0 {
			t.Fatalf("timeout %v: Connect = %v, calls=%d", timeout, err, wire.calls)
		}
	}
}

func TestConnectRejectsNilContextBeforeTraffic(t *testing.T) {
	wire := &cleanupBudgetWire{inner: swdsim.New(sim.New(0x2ba01477))}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	var nilContext context.Context
	if _, err := dp.Connect(nilContext); err == nil || wire.calls != 0 {
		t.Fatalf("Connect(nil) = %v, calls=%d", err, wire.calls)
	}
}

func TestRecoveryUsesConfiguredIndependentBudget(t *testing.T) {
	wire := &cleanupBudgetWire{inner: swdsim.New(sim.New(0x2ba01477))}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)), dap.WithCleanupTimeout(3*time.Second))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	wire.fail = true
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil {
		t.Fatal("missing injected error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := dp.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if wire.entryBudget < 2*time.Second || wire.entryBudget > 3*time.Second {
		t.Fatalf("recovery entry budget = %v", wire.entryBudget)
	}
}
