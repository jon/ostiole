package dap_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func releaseErrorPort(t *testing.T, dp *dap.DebugPort) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dp.Release(ctx); err != nil {
			t.Error(err)
		}
	})
}

func TestRawAPErrorsNameCompleteRegister(t *testing.T) {
	for _, wide := range []bool{false, true} {
		for _, write := range []bool{false, true} {
			for _, completion := range []bool{false, true} {
				t.Run(fmt.Sprintf("v6=%t/write=%t/completion=%t", wide, write, completion), func(t *testing.T) { exerciseRawAPErrorAddress(t, wide, write, completion) })
			}
		}
	}
}

func exerciseRawAPErrorAddress(t *testing.T, wide, write, completion bool) {
	t.Helper()
	target := newWaitTarget()
	sel := dap.NewAPSel(7)
	addr := uint16(0xf8)
	if wide {
		target.Target = dapsim.New(0x4c013477)
		if err := target.SetDPRegister(dap.DPIDR1, 32); err != nil {
			t.Fatal(err)
		}
		var err error
		sel, err = dap.APAt(0x2000)
		if err != nil {
			t.Fatal(err)
		}
		addr = 0xdf8
	}
	if err := target.AddMEMAP(sel, 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	req := apRead(8)
	if write {
		req = apWrite(8)
	}
	if completion {
		if _, err := dp.ReadRawAP(t.Context(), sel.Address(addr)); err != nil {
			t.Fatal(err)
		}
		target.executed[req] = 0
		target.armFault(dpRead(12))
		if wide {
			target.waitSkip = 1
		}
	} else {
		target.armFault(req)
	}
	var err error
	if write {
		err = dp.WriteRawAP(t.Context(), sel.Address(addr), 0)
	} else {
		_, err = dp.ReadRawAP(t.Context(), sel.Address(addr))
	}
	want := fmt.Sprintf("%s register %#x", sel, addr)
	if !errors.Is(err, dap.ErrFault) || !strings.Contains(err.Error(), want) {
		t.Fatalf("access error=%v, want %q and FAULT", err, want)
	}
	wantExecuted := 0
	if completion {
		wantExecuted = 1
	}
	if target.attempts != 1 || target.executed[req] != wantExecuted {
		t.Fatalf("faulted request attempts=%d executions=%d", target.attempts, target.executed[req])
	}
}

func TestJTAGLostStateDoesNotClaimSWDFraming(t *testing.T) {
	_, wire, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	wire.fail = true
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil {
		t.Fatal("missing scan failure")
	}
	before := wire.calls
	_, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err == nil || strings.Contains(err.Error(), "SWD") || !strings.Contains(err.Error(), "framing") || wire.calls != before {
		t.Fatalf("blocked JTAG read=%v, calls %d -> %d", err, before, wire.calls)
	}
}

type retryParityTarget struct {
	*waitTarget
	corrupt bool
}

func (t *retryParityTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := t.waitTarget.Read(ctx, req)
	if err == nil && t.corrupt && req == t.waitFor && t.attempts >= 2 {
		t.corrupt = false
		return value, swd.ErrParity
	}
	return value, err
}

func TestParityAfterWAITKeepsResponseState(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			target := newWaitTarget()
			parityTarget := &retryParityTarget{waitTarget: target}
			wire := swdsim.New(parityTarget)
			dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			target.arm(dpRead(12), 1)
			parityTarget.corrupt = true
			var err error
			if queued {
				txn := dp.NewTxn()
				r := txn.ReadDP(dap.RDBUFF)
				err = txn.Commit(t.Context())
				if !errors.Is(resultError(r), swd.ErrParity) {
					t.Errorf("queued result=%v", resultError(r))
				}
			} else {
				_, err = dp.ReadDP(t.Context(), dap.RDBUFF)
			}
			if !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrWait) || errors.Is(err, swd.ErrWait) {
				t.Fatalf("retry parity=%v", err)
			}
			if target.attempts != 2 || target.executed[dpRead(12)] != 1 || parityTarget.corrupt {
				t.Fatalf("attempts=%d executions=%d armed=%t", target.attempts, target.executed[dpRead(12)], parityTarget.corrupt)
			}
			if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
				t.Fatalf("read after completed parity response=%v", err)
			}
		})
	}
}

func TestAPPostParityAfterWAITRetainsUncertainty(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0: 42})
	parityTarget := &retryParityTarget{waitTarget: target}
	wire := swdsim.New(parityTarget)
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(12)); err != nil {
		t.Fatal(err)
	}
	if err := wire.SetMaxTransferBits(54); err != nil {
		t.Fatal(err)
	}
	target.arm(apRead(12), 1)
	parityTarget.corrupt = true
	_, err := dp.ReadRawAP(t.Context(), apSel(0).Address(12))
	if !errors.Is(err, swd.ErrParity) || !errors.Is(err, dap.ErrIndeterminate) || errors.Is(err, dap.ErrWait) {
		t.Fatalf("accepted AP post parity=%v", err)
	}
	if target.attempts != 2 || target.executed[apRead(12)] != 1 {
		t.Fatalf("attempts=%d executed=%d", target.attempts, target.executed[apRead(12)])
	}
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
		t.Fatalf("known framing after AP parity=%v", err)
	}
}

func TestCancellationDuringWAITClearDropsWAIT(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, caller := range []bool{false, true} {
			t.Run(fmt.Sprintf("queued=%t/caller=%t", queued, caller), func(t *testing.T) { exerciseCanceledWAITClear(t, queued, caller) })
		}
	}
}

func exerciseCanceledWAITClear(t *testing.T, queued, caller bool) {
	t.Helper()
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	nativeErr := errors.New("cleanup receive failed")
	wire := &packedTxnWire{inner: swdsim.New(target), limit: 16384, err: errors.Join(context.DeadlineExceeded, nativeErr)}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatal(err)
	}
	target.arm(apRead(12), 1)
	wire.calls = nil
	wire.failCall = 2
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	want := error(context.DeadlineExceeded)
	if caller {
		wire.cancel = cancel
		wire.cancelCall = 2
		wire.err = nativeErr
		want = context.Canceled
	}
	var err error
	if queued {
		txn := dp.NewTxn()
		r := txn.ReadAPIDR(apSel(0))
		err = txn.Commit(ctx)
		if !errors.Is(resultError(r), want) || errors.Is(resultError(r), dap.ErrWait) {
			t.Errorf("queued result=%v", resultError(r))
		}
	} else {
		_, err = dp.ReadAPIDR(ctx, apSel(0))
	}
	if !errors.Is(err, want) || errors.Is(err, dap.ErrWait) || errors.Is(err, swd.ErrWait) {
		t.Fatalf("canceled WAIT cleanup=%v", err)
	}
	if !errors.Is(err, nativeErr) {
		t.Fatalf("lost native cleanup error: %v", err)
	}
	before := len(target.requests)
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || len(target.requests) != before {
		t.Fatal("ordinary traffic after incomplete cleanup")
	}
	if target.executed[apRead(12)] != 0 || target.attempts != 1 {
		t.Fatalf("failed access replayed: attempts=%d executed=%d", target.attempts, target.executed[apRead(12)])
	}
}
