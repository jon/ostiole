package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestFailedJTAGValidationDoesNotStrandArmOwner(t *testing.T) {
	_, bench, cfg := armJTAGFixture(t, 4, 0)
	bench.taps[0].id = 0x4ba00477
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, bench), cfg)
	if err == nil || c != nil || bench.closes != 1 {
		t.Fatalf("failed chain retained probe without DP state: conn %v closes %d err %v", c, bench.closes, err)
	}
}

func TestFailedJTAGValidationLeavesIdleDebugPort(t *testing.T) {
	model, wire, chain := jtagModelChain(t, 4, 0)
	wire.taps[0].id = 0x4ba00477
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err == nil {
		t.Fatal("mismatched layout connected")
	}
	before := wire.calls
	if err := dp.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatalf("failed chain manufactured restoration: %v", err)
	}
	if len(model.accepted) != 0 || model.aborts != 0 {
		t.Fatal("failed validation reached DP")
	}
	wire.taps[0].id = 0x5ba00477
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatalf("failed validation stranded reconnect: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if wire.state != jtag.Idle {
		t.Fatal("reconnected chain did not release")
	}
}

type releaseFailureWire struct {
	inner   swd.Wire
	fail    bool
	calls   int
	entries int
	cause   error
}

func (w *releaseFailureWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	if bits == 136 {
		w.entries++
	}
	if w.fail {
		w.fail = false
		return nil, w.cause
	}
	return w.inner.SWDIO(ctx, direction, output, bits)
}

type releaseFailureTarget struct {
	*sim.Target
	wire         *releaseFailureWire
	armed        bool
	powerCleared bool
	cancel       context.CancelFunc
}

func (target *releaseFailureTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	err := target.Target.Write(ctx, req, value)
	if err == nil && target.armed && !req.AP && !req.Read && req.Addr == 4 && value&(1<<28|1<<30) == 0 {
		target.powerCleared = true
	}
	return err
}
func (target *releaseFailureTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := target.Target.Read(ctx, req)
	if err == nil && target.powerCleared && !req.AP && req.Read && req.Addr == 4 {
		target.armed, target.powerCleared = false, false
		if target.cancel != nil {
			target.cancel()
		} else {
			target.wire.fail = true
		}
	}
	return value, err
}

func TestFirstDAPReleaseRetryReentersSWD(t *testing.T) {
	target := &releaseFailureTarget{Target: sim.New(0x2ba01477)}
	wire := &releaseFailureWire{inner: swdsim.New(target), cause: errors.New("release failed")}
	target.wire = wire
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.armed = true
	if err := dp.Release(t.Context()); !errors.Is(err, wire.cause) {
		t.Fatalf("lost final SWD release error: %v", err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("failure did not retain SWD restoration")
	}
	before, entries := wire.calls, wire.entries
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("first useful retry failed: %v calls %d -> %d", err, before, wire.calls)
	}
	if wire.entries <= entries || target.OverrunDetectEnabled() {
		t.Fatal("retry did not re-enter and restore SWD")
	}
	calls := wire.calls
	if err := dp.Release(t.Context()); err != nil || wire.calls != calls {
		t.Fatalf("completed release sent traffic: %v", err)
	}
}

func TestDAPReleasePreflightCancellationKeepsKnownSWDFraming(t *testing.T) {
	target := &releaseFailureTarget{Target: sim.New(0x2ba01477)}
	wire := &releaseFailureWire{inner: swdsim.New(target)}
	target.wire = wire
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	target.armed, target.cancel = true, cancel
	if err := dp.Release(ctx); !errors.Is(err, context.Canceled) || !errors.Is(err, swd.ErrNotExecuted) {
		t.Fatalf("release was not rejected before wire: %v", err)
	}
	entries := wire.entries
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if wire.entries != entries || target.OverrunDetectEnabled() {
		t.Fatal("unsent release lost framing or restoration")
	}
}

func TestJTAGEntryAfterAcceptedDPWorkRetainsCleanup(t *testing.T) {
	model, wire, chain := jtagModelChain(t, 4, 0)
	model.ctrl = 1
	model.onAccept = func(req jtagRequest) {
		if !req.ap && !req.read && req.addr == 8 {
			model.onAccept = nil
			wire.failAfter, wire.fail = true, true
		}
	}
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err == nil {
		t.Fatal("missing accepted-entry and repair failures")
	}
	accepted := 0
	for _, req := range model.accepted {
		if !req.ap && !req.read && req.addr == 8 {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("entry SELECT accepted %d times", accepted)
	}
	before := wire.calls
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if wire.calls <= before || wire.state != jtag.Idle || model.ctrl != 1 {
		t.Fatal("accepted DP entry was discarded or inherited state changed")
	}
}
