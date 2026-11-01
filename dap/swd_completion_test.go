package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestCanceledCompletedSELECTRetainsItsObservation(t *testing.T) {
	dp, target, wire := newWideSelectPort(t, 40)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire.cancel = cancel
	if err := dp.WriteDP(ctx, dap.SELECT, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("completed SELECT lost cancellation: %v", err)
	}
	before := len(target.requests)
	if _, err := dp.ReadDP(t.Context(), dap.DLCR); err != nil {
		t.Fatal(err)
	}
	if target.requests[before] != dpRead(12) {
		t.Fatalf("acknowledged SELECT was not settled first: %+v", target.requests[before])
	}
	count := 0
	for _, value := range target.selectValues {
		if value == 5 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("completed SELECT replayed: %v", target.selectValues)
	}
}

func TestCanceledCompletedDPReadReportsCancellation(t *testing.T) {
	wire := &releaseContextWire{inner: swdsim.New(dapsim.New(0x2ba01477))}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire.observe = func(context.Context, int) { cancel() }
	value, err := dp.ReadDP(ctx, dap.DPIDR)
	if !errors.Is(err, context.Canceled) || value != 0x2ba01477 {
		t.Fatalf("completed DPIDR = %#x, %v", value, err)
	}
	wire.observe = nil
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err != nil {
		t.Fatalf("completed read lost framing: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type lifecycleCancelTarget struct {
	*dapsim.Target
	cancel  context.CancelFunc
	release bool
}

func (t *lifecycleCancelTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := t.Target.Read(ctx, req)
	final := value&(1<<29|1<<31) == 1<<29|1<<31
	if t.release {
		final = value&1 == 0
	}
	if err == nil && !req.AP && req.Read && req.Addr == 4 && t.cancel != nil && final {
		t.cancel()
		t.cancel = nil
	}
	return value, err
}

func TestCanceledFinalDAPLifecycleFrameReleasesOnce(t *testing.T) {
	for _, release := range []bool{false, true} {
		target := &lifecycleCancelTarget{Target: dapsim.New(0x2ba01477), release: release}
		wire := &releaseContextWire{inner: swdsim.New(target)}
		dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
		ctx, cancel := context.WithCancel(t.Context())
		if release {
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			target.cancel = cancel
			if err := dp.Release(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Release final frame: %v", err)
			}
		} else {
			target.cancel = cancel
			if _, err := dp.Connect(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Connect final frame: %v", err)
			}
		}
		cancel()
		if dp.CleanupPending() || target.OverrunDetectEnabled() {
			t.Fatal("completed restoration retained ownership or mode")
		}
		before := wire.calls
		if err := dp.Release(t.Context()); err != nil || wire.calls != before {
			t.Fatalf("completed release replayed: %v", err)
		}
	}
}

func TestCanceledFinalMEMAPRestorationDoesNotRepeat(t *testing.T) {
	dp, mem, wire, _ := brokenMEMAPForRelease(t)
	before := wire.calls
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	finalCall := wire.calls - before
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	dp, mem, wire, saved := brokenMEMAPForRelease(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before = wire.calls
	wire.observe = func(context.Context, int) {
		if wire.calls == before+finalCall {
			cancel()
		}
	}
	if err := mem.Release(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("final restoration: %v", err)
	}
	wire.observe = nil
	before = wire.calls
	if err := mem.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatalf("confirmed restoration repeated: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := readReleaseRegisters(t, dp); got != saved {
		t.Fatalf("CSW/TAR = %#x, want %#x", got, saved)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func memAPCompletionFixture(t *testing.T) (*dap.DebugPort, *releaseContextWire) {
	t.Helper()
	target := dapsim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x24770011, nil)
	wire := &releaseContextWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	return dp, wire
}

func TestCanceledFinalMEMAPSnapshotDoesNotReturnAnOwner(t *testing.T) {
	dp, wire := memAPCompletionFixture(t)
	before := wire.calls
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	finalCall := wire.calls - before
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	dp, wire = memAPCompletionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before = wire.calls
	wire.observe = func(context.Context, int) {
		if wire.calls == before+finalCall {
			cancel()
		}
	}
	mem, err = dap.OpenMemAP(ctx, dp, apSel(0))
	if mem != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot acquired despite cancellation: %v, %v", mem, err)
	}
	wire.observe = nil
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledFinalDebugBaseReadDoesNotReturnAnAddress(t *testing.T) {
	dp, wire := memAPCompletionFixture(t)
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	if _, _, err := mem.ReadDebugBase(t.Context()); err != nil {
		t.Fatal(err)
	}
	before = wire.calls
	if _, _, err := mem.ReadDebugBase(t.Context()); err != nil {
		t.Fatal(err)
	}
	finalCall := wire.calls - before
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before = wire.calls
	wire.observe = func(context.Context, int) {
		if wire.calls == before+finalCall {
			cancel()
		}
	}
	addr, present, err := mem.ReadDebugBase(ctx)
	if addr != 0 || present || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery = %#x,%v,%v", addr, present, err)
	}
	wire.observe = nil
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledFinalTxnRetainsConfirmedResult(t *testing.T) {
	wire := &releaseContextWire{inner: swdsim.New(dapsim.New(0x2ba01477))}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire.observe = func(context.Context, int) { cancel() }
	txn := dp.NewTxn()
	result := txn.ReadDP(dap.DPIDR)
	err := txn.Commit(ctx)
	value, physical := result.Value()
	if physical != nil || value != 0x2ba01477 || !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit=%v; confirmed result=%#x/%v; want caller cancellation", err, value, physical)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type cancelFaultTarget struct {
	*dapsim.Target
	cancel context.CancelFunc
}

func (t *cancelFaultTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	if req.AP && req.Read && req.Addr == 12 && t.cancel != nil {
		t.cancel()
		t.cancel = nil
		return swd.ErrFault
	}
	return t.Target.Acknowledge(ctx, req)
}
func TestCompletedFAULTKeepsCallerCancellation(t *testing.T) {
	target := &cancelFaultTarget{Target: dapsim.New(0x2ba01477)}
	if err := target.AddMEMAP(dap.NewAPSel(0), 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	target.cancel = cancel
	_, err := dp.ReadAPIDR(ctx, dap.NewAPSel(0))
	if !errors.Is(err, dap.ErrFault) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadAPIDR=%v; want physical FAULT and caller cancellation", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledFinalScalarCompletionReportsCancellation(t *testing.T) {
	for _, write := range []bool{false, true} {
		dp, wire := memAPCompletionFixture(t)
		mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
		if err != nil {
			t.Fatal(err)
		}
		operation := func(ctx context.Context) error {
			if write {
				return mem.WriteScalar(ctx, 0x100, dap.Size32, 0x13579bdf)
			}
			_, err := mem.ReadScalar(ctx, 0x100, dap.Size32)
			return err
		}
		if err := operation(t.Context()); err != nil {
			t.Fatal(err)
		}
		before := wire.calls
		if err := operation(t.Context()); err != nil {
			t.Fatal(err)
		}
		finalCall := wire.calls - before
		ctx, cancel := context.WithCancel(t.Context())
		before = wire.calls
		wire.observe = func(context.Context, int) {
			if wire.calls == before+finalCall {
				cancel()
			}
		}
		err = operation(ctx)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write%v final scalar: %v", write, err)
		}
		wire.observe = nil
		if err := mem.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := dp.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
