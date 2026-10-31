package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type stalledAPTarget struct {
	*waitTarget
	stalled   bool
	cancel    context.CancelFunc
	starts    int
	aborts    int
	abortErr  error
	maxAborts int
}

func (t *stalledAPTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	if err := t.waitTarget.Acknowledge(ctx, req); err != nil {
		return err
	}
	if t.stalled && (req.AP || req == dpWrite(8) || req == dpRead(12)) {
		return swd.ErrWait
	}
	return nil
}

func (t *stalledAPTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := t.waitTarget.Read(ctx, req)
	if req.AP {
		t.starts++
		if t.cancel != nil {
			t.stalled = true
			t.cancel()
			t.cancel = nil
		}
	}
	return value, err
}

func (t *stalledAPTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req == dpWrite(0) && value&1 != 0 {
		t.aborts++
		if t.maxAborts != 0 && t.aborts > t.maxAborts {
			return errors.New("recovery restarted DAPABORT")
		}
		if t.abortErr != nil {
			return t.abortErr
		}
		t.stalled = false
	}
	return t.waitTarget.Write(ctx, req, value)
}

func TestReleaseAbortsAPLeftPendingByCancellation(t *testing.T) {
	target := &stalledAPTarget{waitTarget: newWaitTarget()}
	addAP(t, target, 1, 0x24770011)
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	target.cancel = cancel
	if _, err := dp.ReadRawAP(ctx, apSel(1).Address(0xfc)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadRawAP() = %v, want cancellation", err)
	}
	starts := target.starts
	if !target.stalled || starts != 1 {
		t.Fatalf("stalled=%t accepted AP reads=%d", target.stalled, starts)
	}
	if err := dp.Release(t.Context()); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("first Release() = %v, want original WAIT", err)
	}
	if target.stalled || target.aborts != 1 {
		t.Fatalf("stalled=%t aborts=%d", target.stalled, target.aborts)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.starts != starts {
		t.Fatal("cleanup replayed interrupted AP read")
	}
	state, err := target.Target.Read(t.Context(), dpRead(4))
	if err != nil {
		t.Fatal(err)
	}
	if state&allPower != 0 {
		t.Fatalf("actual power after release = %#x", state)
	}
}

func TestAbortRecoveryFromBankedRDBUFFKeepsResponseGrammar(t *testing.T) {
	target := newWaitTarget()
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DLCR); err != nil {
		t.Fatal(err)
	}
	target.arm(dpRead(12), -1)
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("ReadDP(RDBUFF) = %v, want WAIT", err)
	}
	assertDAPABORT(t, target)
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, overrunDetect|debugRequest|systemRequest); err != nil {
		t.Fatalf("preserving actual ORUNDETECT after abort: %v", err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSELECTWaitAbortFailureRetainsErrorAndStaleClients(t *testing.T) {
	target := &stalledAPTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0x100: 42})
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.stalled = true
	target.abortErr = errors.New("injected DAPABORT failure")
	err = dp.WriteDP(t.Context(), dap.SELECT, 0)
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, target.abortErr) {
		t.Fatalf("WriteDP(SELECT) = %v, want WAIT and abort failure", err)
	}
	before := len(target.requests)
	if _, err := mem.ReadWord(t.Context(), 0x100); err == nil {
		t.Fatal("stale MEM-AP remained usable")
	}
	if len(target.requests) != before {
		t.Fatal("stale client sent traffic")
	}
	target.abortErr = nil
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0x100); err == nil {
		t.Fatal("cleanup revalidated stale MEM-AP")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPostAbortWAITDoesNotRestartRecovery(t *testing.T) {
	target := &stalledAPTarget{waitTarget: newWaitTarget(), maxAborts: 1}
	addAP(t, target, 0, 0x24770011)
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(2))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.arm(apRead(12), -1)
	target.selectWaits = -1
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrWait) {
		t.Fatalf("ReadAPIDR() = %v, want WAIT", err)
	}
	if target.aborts != 1 || target.selectAttempts != 2 {
		t.Fatalf("aborts=%d recovery SELECT attempts=%d", target.aborts, target.selectAttempts)
	}
	target.selectWaits = 0
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type minimalAbortTarget struct{ *waitTarget }

func (m *minimalAbortTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := m.waitTarget.Read(ctx, req)
	if m.aborted && req == dpRead(4) {
		value |= 1 << 4
	}
	return value, err
}

func TestAbortUsesCurrentEntryMinimalIdentity(t *testing.T) {
	target := &minimalAbortTarget{waitTarget: newWaitTarget()}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.dpidrOverride = 0x2ba11477
	target.SetOverrunDetect(true)
	target.stickyAfterAbort = testWriteDataError
	target.arm(dpWrite(4), -1)
	before := len(target.abortValues)
	if _, err := dp.Connect(t.Context()); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("Connect() = %v, want WAIT", err)
	}
	sawAbort := false
	for _, value := range target.abortValues[before:] {
		if value&1 != 0 {
			sawAbort = true
		}
		if sawAbort && value&2 != 0 {
			t.Fatalf("Minimal DP reserved STKCMPCLR written: %#v", target.abortValues[before:])
		}
	}
	if !sawAbort {
		t.Fatal("missing abort during second entry")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPendingSELECTBarrierStallCanReleaseWithoutAPReplay(t *testing.T) {
	target := &stalledAPTarget{waitTarget: newWaitTarget()}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
		t.Fatal(err)
	}
	target.stalled = true
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("SELECT barrier = %v, want WAIT", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.stalled || target.aborts != 1 || target.starts != 0 {
		t.Fatalf("stalled=%t aborts=%d AP reads=%d", target.stalled, target.aborts, target.starts)
	}
	state, err := target.Target.Read(t.Context(), dpRead(4))
	if err != nil {
		t.Fatal(err)
	}
	if state&allPower != 0 {
		t.Fatalf("actual power after release = %#x", state)
	}
}
