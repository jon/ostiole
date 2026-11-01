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
	inner         swd.Wire
	fail          bool
	calls         int
	entryDeadline time.Time
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
			w.entryDeadline = deadline
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

func TestReleaseReentryUsesCallerBudget(t *testing.T) {
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
	before := wire.calls
	if err := dp.Release(ctx); !errors.Is(err, context.Canceled) || wire.calls != before {
		t.Fatalf("canceled release = %v, wire calls = %d; want %d", err, wire.calls, before)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if err := dp.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if wire.entryDeadline != deadline {
		t.Fatalf("release entry deadline = %v, want %v", wire.entryDeadline, deadline)
	}
}

type recoveryBudgetTarget struct {
	*waitTarget
	abortBudget time.Duration
}

func (target *recoveryBudgetTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req == dpWrite(0) && value == 1 {
		deadline, _ := ctx.Deadline()
		target.abortBudget = time.Until(deadline)
	}
	return target.waitTarget.Write(ctx, req, value)
}

func TestRecoveryUsesConfiguredIndependentBudget(t *testing.T) {
	target := &recoveryBudgetTarget{waitTarget: newWaitTarget()}
	addAP(t, target.waitTarget, 0, 0x24770011)
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target), swd.WithCompletionTimeout(3*time.Second))),
		dap.WithMaxWaits(1), dap.WithCleanupTimeout(3*time.Second))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	target.arm(apRead(12), 1)
	if _, err := dp.ReadAPIDR(ctx, dap.NewAPSel(0)); !errors.Is(err, dap.ErrWait) {
		t.Fatalf("AP WAIT = %v", err)
	}
	if target.abortBudget < 2*time.Second || target.abortBudget > 3*time.Second {
		t.Fatalf("independent recovery budget = %v", target.abortBudget)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
