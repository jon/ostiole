package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestSWDAcceptedWriteCompletionWAITIsIndeterminate(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "immediate"
		if queued {
			name = "transaction"
		}
		t.Run(name, func(t *testing.T) {
			target := newWaitTarget()
			addMEMAP(t, target, 0, 0x24770011, nil)
			dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			addr := apSel(0).Address(4)
			if err := dp.WriteRawAP(t.Context(), addr, 0); err != nil {
				t.Fatal(err)
			}
			before := target.executed[apWrite(4)]
			target.arm(dpRead(12), 100)
			var err error
			if queued {
				txn := dp.NewTxn()
				result := txn.WriteRawAP(addr, 0x1234)
				err = txn.Commit(t.Context())
				if !errors.Is(result.Err(), dap.ErrIndeterminate) {
					t.Fatalf("write result = %v", result.Err())
				}
			} else {
				err = dp.WriteRawAP(t.Context(), addr, 0x1234)
			}
			if !errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) {
				t.Fatalf("accepted write completion error = %v, want WAIT and indeterminate", err)
			}
			if got := target.executed[apWrite(4)] - before; got != 1 {
				t.Fatalf("accepted writes = %d, want 1", got)
			}
			target.armed = false
			if err := dp.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSWDRawPostingParityInvalidatesMemAP(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, nil)
	wire := &readParityWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	// The managed access establishes bank zero without invalidating the owner.
	if _, err := mem.ReadScalar(t.Context(), 0, dap.Size32); err != nil {
		t.Fatal(err)
	}
	before := target.executed[apRead(0)]
	wire.armed = true
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0)); !errors.Is(err, swd.ErrParity) {
		t.Fatalf("posting error = %v", err)
	}
	if got := target.executed[apRead(0)] - before; got != 1 {
		t.Fatalf("posting reads = %d, want 1", got)
	}
	assertMEMAPInvalidated(t, mem)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSWDRejectedWriteDoesNotReportIndeterminate(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, nil)
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	addr := apSel(0).Address(4)
	if err := dp.WriteRawAP(t.Context(), addr, 0); err != nil {
		t.Fatal(err)
	}
	target.arm(apWrite(4), 100)
	err := dp.WriteRawAP(t.Context(), addr, 0x1234)
	if !errors.Is(err, swd.ErrWait) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("rejected write error = %v", err)
	}
	if target.executed[apWrite(4)] != 0 {
		t.Fatal("rejected write executed")
	}
	target.armed = false
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSWDWriteCompletionParityConfirmsAcceptance(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, nil)
	wire := &readParityWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	addr := apSel(0).Address(4)
	if err := dp.WriteRawAP(t.Context(), addr, 0); err != nil {
		t.Fatal(err)
	}
	before := target.executed[apWrite(4)]
	wire.armed = true
	err := dp.WriteRawAP(t.Context(), addr, 0x1234)
	if !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("completed write error = %v", err)
	}
	if got := target.executed[apWrite(4)] - before; got != 1 {
		t.Fatalf("accepted writes = %d", got)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type cancelAcceptedAPTarget struct {
	*waitTarget
	cancel context.CancelFunc
}

func (target *cancelAcceptedAPTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	err := target.waitTarget.Write(ctx, req, value)
	if err == nil && target.cancel != nil && req == apWrite(4) {
		target.cancel()
		target.cancel = nil
	}
	return err
}

func TestSWDWriteCancellationDistinguishesUnsentAndAccepted(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "unsent"
		if accepted {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			target := &cancelAcceptedAPTarget{waitTarget: newWaitTarget()}
			addMEMAP(t, target.waitTarget, 0, 0x24770011, nil)
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			addr := apSel(0).Address(4)
			if err := dp.WriteRawAP(t.Context(), addr, 0); err != nil {
				t.Fatal(err)
			}
			before := target.executed[apWrite(4)]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if accepted {
				target.cancel = cancel
			} else {
				cancel()
			}
			err := dp.WriteRawAP(ctx, addr, 0x1234)
			if !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrIndeterminate) != accepted {
				t.Fatalf("accepted=%t, error=%v", accepted, err)
			}
			want := 0
			if accepted {
				want = 1
			}
			if got := target.executed[apWrite(4)] - before; got != want {
				t.Fatalf("writes = %d, want %d", got, want)
			}
			if err := dp.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type faultStateParityTarget struct {
	*waitTarget
	wire  *readParityWire
	armed bool
}

func (t *faultStateParityTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	if t.armed && req == dpRead(4) {
		t.wire.armed = true
		t.armed = false
	}
	return t.waitTarget.Acknowledge(ctx, req)
}
func TestSWDWriteCompletionFaultCleanupParityIsIndeterminate(t *testing.T) {
	t.Run("immediate", func(t *testing.T) { exerciseCompletionFaultParity(t, false) })
	t.Run("transaction", func(t *testing.T) { exerciseCompletionFaultParity(t, true) })
}

func exerciseCompletionFaultParity(t *testing.T, queued bool) {
	t.Helper()
	target := &faultStateParityTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target.waitTarget, 0, 0x24770011, nil)
	wire := &readParityWire{inner: swdsim.New(target)}
	target.wire = wire
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	addr := apSel(0).Address(4)
	if err := dp.WriteRawAP(t.Context(), addr, 0); err != nil {
		t.Fatal(err)
	}
	before := target.executed[apWrite(4)]
	target.armFault(dpRead(12))
	target.armed = true
	var err error
	if queued {
		txn := dp.NewTxn()
		result := txn.WriteRawAP(addr, 0x1234)
		err = txn.Commit(t.Context())
		if !errors.Is(result.Err(), dap.ErrIndeterminate) {
			t.Errorf("queued result = %v", result.Err())
		}
	} else {
		err = dp.WriteRawAP(t.Context(), addr, 0x1234)
	}
	if got := target.executed[apWrite(4)] - before; got != 1 {
		t.Fatalf("accepted writes=%d", got)
	}
	if !errors.Is(err, dap.ErrFault) || !errors.Is(err, swd.ErrParity) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("unconfirmed write completion=%v; want fault, cleanup parity, and indeterminate", err)
	}
}

type lostAPCaptureWire struct {
	swd.Wire
	target *waitTarget
	armed  bool
	cause  error
}

func (w *lostAPCaptureWire) SWDIO(ctx context.Context, d, o []byte, b int) ([]byte, error) {
	before := w.target.executed[apWrite(4)]
	input, err := w.Wire.SWDIO(ctx, d, o, b)
	if err == nil && w.armed && w.target.executed[apWrite(4)] > before {
		w.armed = false
		return nil, w.cause
	}
	return input, err
}
func TestSWDAcceptedWriteLostCaptureIsIndeterminate(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, nil)
	primary := errors.New("lost capture after accepted write")
	wire := &lostAPCaptureWire{Wire: swdsim.New(target), target: target, cause: primary}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	addr := apSel(0).Address(4)
	if err := dp.WriteRawAP(t.Context(), addr, 0); err != nil {
		t.Fatal(err)
	}
	before := target.executed[apWrite(4)]
	wire.armed = true
	err := dp.WriteRawAP(t.Context(), addr, 0x1234)
	if got := target.executed[apWrite(4)] - before; got != 1 {
		t.Fatalf("accepted writes=%d", got)
	}
	if !errors.Is(err, primary) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("accepted write with lost capture=%v; want native cause and dap.ErrIndeterminate", err)
	}
}
