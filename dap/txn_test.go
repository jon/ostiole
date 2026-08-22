package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type readParityWire struct {
	inner swd.Wire
	armed bool
}

type rdbuffWaitTarget struct {
	*waitTarget
	active bool
	skip   int
	seen   int
}

func (t *rdbuffWaitTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	if t.active && req == (dpRead(0x0c)) {
		if t.seen >= t.skip {
			return swd.ErrWait
		}
		t.seen++
	}
	return t.waitTarget.Acknowledge(ctx, req)
}

func (w *readParityWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	if err == nil && w.armed && bits == 42 && direction[0]&0x02 == 0 {
		w.armed = false
		input[32/8] ^= 1 << (32 % 8)
	}
	if err == nil && w.armed && bits == 54 && output[0]&0x04 != 0 {
		w.armed = false
		input[44/8] ^= 1 << (44 % 8)
	}
	return input, err
}

func TestDebugPortTransactionResolvesOrderedOperations(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	txn := dp.NewTxn()
	dpidr := txn.ReadDP(dap.DPIDR)
	idr := txn.ReadAPIDR(apSel(0))
	write := txn.WriteRawAP(apSel(0).Address(0x00), 0x23000040)
	csw := txn.ReadRawAP(apSel(0).Address(0x00))
	if _, err := dpidr.Value(); !errors.Is(err, dap.ErrResultPending) {
		t.Fatalf("Value() before Commit error = %v, want pending", err)
	}
	if err := write.Err(); !errors.Is(err, dap.ErrResultPending) {
		t.Fatalf("Err() before Commit = %v, want pending", err)
	}
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, dpidr, 0x2ba01477)
	assertTxnValue(t, idr, 0x24770011)
	if err := write.Err(); err != nil {
		t.Fatalf("write result error = %v", err)
	}
	assertTxnValue(t, csw, 0x23000040)
	if err := txn.Commit(t.Context()); !errors.Is(err, dap.ErrTxnCommitted) {
		t.Fatalf("second Commit() error = %v, want committed", err)
	}
	if _, err := txn.ReadDP(dap.DPIDR).Value(); !errors.Is(err, dap.ErrTxnCommitted) {
		t.Fatalf("queue after Commit error = %v, want committed", err)
	}
	if err := txn.WriteDP(dap.ABORT, 0).Err(); !errors.Is(err, dap.ErrTxnCommitted) {
		t.Fatalf("queue write after Commit error = %v, want committed", err)
	}
}

func TestDebugPortTransactionSettlesSELECTBeforeAPRequest(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	txn := dp.NewTxn()
	idr := txn.ReadAPIDR(apSel(0))
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, idr, 0x24770011)
	want := []swdsim.Request{dpWrite(0x08), dpRead(0x0c), apRead(0x0c), dpRead(0x0c)}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("queued AP read requests = %#v, want %#v", got, want)
	}
}

func TestTransactionResultZeroValuesArePending(t *testing.T) {
	var read dap.ReadResult
	if _, err := read.Value(); !errors.Is(err, dap.ErrResultPending) {
		t.Fatalf("zero ReadResult.Value() error = %v, want pending", err)
	}
	var write dap.WriteResult
	if err := write.Err(); !errors.Is(err, dap.ErrResultPending) {
		t.Fatalf("zero WriteResult.Err() = %v, want pending", err)
	}
}

func TestDebugPortTransactionSettlesSELECTBeforeBankedDPAccess(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0x120000f0); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	result := txn.ReadDP(dap.DLCR)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := result.Value(); err != nil {
		t.Fatal(err)
	}
	want := []swdsim.Request{dpRead(0x0c), dpWrite(0x08), dpRead(0x0c), dpRead(0x04)}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("transaction requests = %#v, want %#v", got, want)
	}
}

func TestDebugPortTransactionDistinguishesBankIndependentAndBankZero(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0x120000f1); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	dpidr := txn.ReadDP(dap.DPIDR)
	state := txn.ReadDP(dap.CTRLSTAT)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, dpidr, 0x2ba01477)
	if _, err := state.Value(); err != nil {
		t.Fatal(err)
	}
	want := []swdsim.Request{dpRead(0x00), dpWrite(0x08), dpRead(0x0c), dpRead(0x04)}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("transaction requests = %#v, want %#v", got, want)
	}
	if got := target.selectValues[len(target.selectValues)-1]; got != 0x120000f0 {
		t.Fatalf("SELECT for queued CTRL/STAT = %#08x, want 0x120000f0", got)
	}
}

func TestDebugPortTransactionPreservesConfirmedSELECTAfterABORT(t *testing.T) {
	target := newWaitTarget()
	const dlcr = uint32(0xa5a50000)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := target.SetDPRegister(dap.DLCR, dlcr); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0x120000f0); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); err != nil {
		t.Fatal(err)
	}

	txn := dp.NewTxn()
	abort := txn.WriteDP(dap.ABORT, 0)
	read := txn.ReadDP(dap.DLCR)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnWrite(t, abort)
	assertTxnValue(t, read, dlcr)
	if got := target.selectValues[len(target.selectValues)-1]; got != 0x120000f1 {
		t.Fatalf("SELECT after ABORT = %#08x, want preserved AP fields", got)
	}
}

func TestDebugPortTransactionValidatesBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	valid := txn.ReadAPIDR(apSel(0))
	var zeroSel dap.APSel
	invalidSelector := txn.ReadAPIDR(zeroSel)
	invalid := txn.ReadRawAP(apSel(0).Address(0x02))
	invalidWrite := txn.WriteRawAP(apSel(0).Address(0xfc), 1)
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded with invalid raw AP operations")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after failed validation = %d, want %d", got, before)
	}
	if _, err := valid.Value(); err != dap.ErrNotExecuted {
		t.Fatalf("valid result error = %v, want not executed", err)
	}
	if _, err := invalidSelector.Value(); err == nil || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("zero-selector result error = %v, want validation error", err)
	}
	if _, err := invalid.Value(); err == nil || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("invalid result error = %v, want validation error", err)
	}
	if err := invalidWrite.Err(); err == nil || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("APIDR write result error = %v, want validation error", err)
	}
}

func TestDebugPortTransactionAttributesAccessPreflightFailure(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)

	before := len(target.requests)
	txn := dp.NewTxn()
	prefix := txn.ReadDP(dap.DPIDR)
	inaccessible := txn.ReadAPIDR(apSel(0))
	suffix := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded without a connected debug port")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after failed access preflight = %d, want %d", got, before)
	}
	if _, err := prefix.Value(); err == nil || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("prefix result error = %v, want connection error", err)
	}
	if _, err := inaccessible.Value(); err != dap.ErrNotExecuted {
		t.Fatalf("inaccessible AP result error = %v, want not executed", err)
	}
	if _, err := suffix.Value(); err != dap.ErrNotExecuted {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func TestDebugPortTransactionRejectsReadOnlyDPWriteBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	target.dpidrOverride = 0x2ba02477
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, reg := range []dap.DPRegister{dap.DPIDR, dap.TARGETID, dap.DLPIDR, dap.EVENTSTAT, dap.RESEND, dap.RDBUFF} {
		before := len(target.requests)
		txn := dp.NewTxn()
		result := txn.WriteDP(reg, 0)
		if err := txn.Commit(t.Context()); err == nil {
			t.Fatalf("Commit() succeeded with read-only DP write %s", reg)
		}
		if got := len(target.requests); got != before {
			t.Fatalf("requests after rejected DP write = %d, want %d", got, before)
		}
		if err := result.Err(); err == nil {
			t.Fatalf("DP write result for %s succeeded", reg)
		}
	}

	for _, reg := range []dap.DPRegister{dap.ABORT, dap.SELECT} {
		before := len(target.requests)
		txn := dp.NewTxn()
		result := txn.ReadDP(reg)
		if err := txn.Commit(t.Context()); err == nil {
			t.Fatalf("Commit() succeeded with write-only DP read %s", reg)
		}
		if got := len(target.requests); got != before {
			t.Fatalf("requests after rejected DP read = %d, want %d", got, before)
		}
		if _, err := result.Value(); err == nil {
			t.Fatalf("DP read result for %s succeeded", reg)
		}
	}
}

func TestDebugPortTransactionRejectsUnknownDPRegistersBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)

	before := len(target.requests)
	txn := dp.NewTxn()
	read := txn.ReadDP(0)
	write := txn.WriteDP(0xffff, 0)
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded with unknown DP registers")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after rejected DP operations = %d, want %d", got, before)
	}
	if _, err := read.Value(); err == nil {
		t.Fatal("unknown DP read result succeeded")
	}
	if err := write.Err(); err == nil {
		t.Fatal("unknown DP write result succeeded")
	}
}

func TestDebugPortTransactionRejectsUnsupportedTurnaroundBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	read := txn.ReadDP(dap.DPIDR)
	write := txn.WriteDP(dap.DLCR, 1<<8)
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("Commit() accepted unsupported turnaround")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after rejected DP write = %d, want %d", got, before)
	}
	if _, err := read.Value(); err != dap.ErrNotExecuted {
		t.Fatalf("DPIDR result error = %v, want not executed", err)
	}
	if err := write.Err(); err == nil {
		t.Fatal("DP write result succeeded")
	}
}

func TestDebugPortTransactionPreservesConnectionOwnedOverrunDetection(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.CTRLSTAT, state)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := write.Err(); err != nil {
		t.Fatal(err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("queued CTRL/STAT write cleared ORUNDETECT")
	}
}

func TestDebugPortTransactionRejectsOverrunChangeBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	txn := dp.NewTxn()
	read := txn.ReadDP(dap.DPIDR)
	write := txn.WriteDP(dap.CTRLSTAT, state&^overrunDetect)
	if err := txn.Commit(t.Context()); err == nil || write.Err() == nil {
		t.Fatal("Commit() accepted a connection-owned ORUNDETECT change")
	}
	if _, err := read.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("DPIDR result error = %v, want not executed", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("invalid transaction sent %d requests", got-before)
	}
}

func TestDebugPortTransactionKeepsFramingAfterNonTransitionCTRLSTATBarrierWAIT(t *testing.T) {
	testNonTransitionCTRLSTATBarrierWAIT(t, 2, 1, true)
}

func testNonTransitionCTRLSTATBarrierWAIT(t *testing.T, writeCount, skip int, wantOverrun bool) {
	t.Helper()
	target := &rdbuffWaitTarget{waitTarget: newWaitTarget(), skip: skip}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	if wantOverrun {
		state |= overrunDetect
	}
	txn := dp.NewTxn()
	writes := make([]*dap.WriteResult, writeCount)
	for i := range writes {
		writes[i] = txn.WriteDP(dap.CTRLSTAT, state)
	}
	target.active = true
	err = txn.Commit(t.Context())
	if !errors.Is(err, swd.ErrWait) {
		t.Fatalf("Commit() error = %v, want WAIT", err)
	}
	for i := range writes[:len(writes)-1] {
		if err := writes[i].Err(); err != nil {
			t.Fatalf("write %d error = %v", i, err)
		}
	}
	if err := writes[len(writes)-1].Err(); !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("last write error = %v, want indeterminate", err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err != nil {
		t.Fatalf("ReadDP(DPIDR) after non-transition WAIT: %v", err)
	}
	if got := target.OverrunDetectEnabled(); got != wantOverrun {
		t.Fatalf("ORUNDETECT = %t, want %t", got, wantOverrun)
	}
}

func TestDebugPortTransactionRejectsCTRLSTATWriteWithoutKnownResponse(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	txn := dp.NewTxn()
	result := txn.WriteDP(dap.CTRLSTAT, 0)
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("Commit() accepted CTRL/STAT write without a known response grammar")
	}
	if err := result.Err(); err == nil {
		t.Fatal("CTRL/STAT result succeeded")
	}
	if len(target.requests) != 0 {
		t.Fatalf("requests after rejected transaction = %d, want 0", len(target.requests))
	}
}

func TestDebugPortTransactionRejectsOverrunChangeBeforeTargetFailure(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	target.dropWriteFor = dpWrite(0x04)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	before := len(target.requests)
	txn := dp.NewTxn()
	result := txn.WriteDP(dap.CTRLSTAT, state&^overrunDetect)
	if err := txn.Commit(t.Context()); err == nil || result.Err() == nil {
		t.Fatal("Commit() accepted a connection-owned ORUNDETECT change")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("rejected transaction sent %d requests", got-before)
	}
}

func TestDebugPortTransactionRejectsOverrunChangeBeforeWireTraffic(t *testing.T) {
	target := newWaitTarget()
	wire := &readParityWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	wire.armed = true
	txn := dp.NewTxn()
	result := txn.WriteDP(dap.CTRLSTAT, state&^overrunDetect)
	if err := txn.Commit(t.Context()); err == nil || result.Err() == nil {
		t.Fatal("Commit() accepted a connection-owned ORUNDETECT change")
	}
	if !wire.armed || len(target.requests) != before {
		t.Fatal("rejected ORUNDETECT change reached the wire")
	}
}

func TestDebugPortTransactionRejectsDPv3BankWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	target.dpidrOverride = 0x2ba03477
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	result := txn.ReadDP(dap.DLCR)
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("transaction accepted an ADIv5 banked address on DPv3")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after rejected DPv3 transaction = %d, want %d", got, before)
	}
	if _, err := result.Value(); err == nil || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("DPv3 result error = %v, want address validation error", err)
	}
}

func TestDebugPortTransactionKeepsConfirmedPrefix(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.armFault(apRead(0x0c))
	txn := dp.NewTxn()
	prefix := txn.ReadDP(dap.DPIDR)
	faulted := txn.ReadAPIDR(apSel(0))
	suffix := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Commit() error = %v, want FAULT", err)
	}
	assertTxnValue(t, prefix, 0x2ba01477)
	if _, err := faulted.Value(); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("faulted result error = %v, want FAULT", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func TestDebugPortTransactionDoesNotConfirmAbandonedDPWrite(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.dropWriteFor = dpWrite(0x08)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.SELECT, 0x12000000)
	suffix := txn.ReadAPIDR(apSel(0))
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Commit() error = %v, want FAULT", err)
	}
	if err := write.Err(); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("DP write result error = %v, want FAULT", err)
	}
	if err := write.Err(); errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("DP write result error = %v, want known abandoned write", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func TestDebugPortTransactionSettlesDPWriteThroughRDBUFF(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.SELECT, 0)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnWrite(t, write)
	want := []swdsim.Request{dpWrite(0x08), dpRead(0x0c)}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("DP write requests = %#v, want %#v", got, want)
	}
}

func TestDebugPortTransactionKeepsFaultDeterminateWhenCleanupLosesFraming(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	cleanupErr := errors.New("injected CTRL/STAT read failure")
	target.armFault(apRead(0x0c))
	target.ctrlStatErr = cleanupErr
	txn := dp.NewTxn()
	faulted := txn.ReadAPIDR(apSel(0))
	suffix := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Commit() error = %v, want FAULT and CTRL/STAT read failure", err)
	}
	if _, err := faulted.Value(); !errors.Is(err, swd.ErrFault) || !errors.Is(err, cleanupErr) {
		t.Fatalf("AP result error = %v, want FAULT and CTRL/STAT read failure", err)
	}
	if _, err := faulted.Value(); errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("AP result error = %v, want rejected request", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func TestDebugPortTransactionKeepsWAITDeterminateWhenDAPABORTFails(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	abortErr := errors.New("injected DAPABORT failure")
	target.abortErr = abortErr
	target.arm(apRead(0x0c), -1)
	txn := dp.NewTxn()
	waited := txn.ReadAPIDR(apSel(0))
	suffix := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrWait) || !errors.Is(err, abortErr) {
		t.Fatalf("Commit() error = %v, want WAIT and DAPABORT failure", err)
	}
	if _, err := waited.Value(); !errors.Is(err, swd.ErrWait) || !errors.Is(err, abortErr) {
		t.Fatalf("AP result error = %v, want WAIT and DAPABORT failure", err)
	}
	if _, err := waited.Value(); errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("AP result error = %v, want rejected request", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func TestDebugPortTransactionSettlesPreviousImmediateDPWriteBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.dropWriteFor = dpWrite(0x08)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0); err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	abort := txn.WriteDP(dap.ABORT, 1)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Commit() error = %v, want previous write FAULT", err)
	}
	if err := abort.Err(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("DAPABORT result error = %v, want not executed", err)
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, queued DAPABORT must not execute", target.abortValues)
		}
	}
}

func TestDebugPortTransactionInvalidatesAPAfterDPWriteBarrierWAIT(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	target.arm(dpRead(0x0c), -1)
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.SELECT, 0)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want WAIT and indeterminate DP write", err)
	}
	if err := write.Err(); !errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("DP write result error = %v, want WAIT and indeterminate outcome", err)
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, DP write barrier must not cause DAPABORT", target.abortValues)
		}
	}
	target.armed = false
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err == nil {
		t.Fatal("ReadWord() succeeded after uncertain DP write barrier")
	}
}

func TestDebugPortTransactionInvalidatesAPAfterSELECTBarrierWAIT(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	target.arm(dpRead(0x0c), -1)
	txn := dp.NewTxn()
	read := txn.ReadDP(dap.DLCR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want WAIT and indeterminate SELECT barrier", err)
	}
	if _, err := read.Value(); !errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("banked DP result error = %v, want WAIT and indeterminate outcome", err)
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, SELECT barrier must not cause DAPABORT", target.abortValues)
		}
	}
	target.armed = false
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err == nil {
		t.Fatal("ReadWord() succeeded after uncertain SELECT barrier")
	}
}

func TestDebugPortImmediateRDBUFFInvalidatesAPAfterDPWriteWAIT(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0); err != nil {
		t.Fatal(err)
	}

	target.arm(dpRead(0x0c), -1)
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("ReadDP(RDBUFF) error = %v, want WAIT", err)
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, immediate DP barrier must not cause DAPABORT", target.abortValues)
		}
	}
	target.armed = false
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err == nil {
		t.Fatal("ReadWord() succeeded after uncertain DP write barrier")
	}
}

func TestDebugPortTransactionDoesNotInvalidateAPForDPWriteBarrierFAULT(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	target.dropWriteFor = dpWrite(0x08)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.SELECT, 0)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Commit() error = %v, want WDATAERR FAULT", err)
	}
	if err := write.Err(); !errors.Is(err, swd.ErrFault) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("DP write result error = %v, want rejected write", err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatalf("ReadWord() after DP write barrier FAULT: %v", err)
	}
}

func TestDebugPortTransactionKeepsRejectedAPWriteDeterminate(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	target.dropWriteFor = apWrite(0x00 & 0x0c)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	txn := dp.NewTxn()
	write := txn.WriteRawAP(apSel(0).Address(0x00), 0x23000040)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want rejected AP write", err)
	}
	if err := write.Err(); !errors.Is(err, swd.ErrFault) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("AP write result error = %v, want rejected write", err)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after rejected queued write: %v", err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	value, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x00))
	if err != nil {
		t.Fatal(err)
	}
	if value != 0 {
		t.Fatalf("AP CSW after rejected write = %#08x, want unchanged", value)
	}
}

func TestDebugPortTransactionDoesNotInvalidateAPForRejectedDAPABORT(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	target.dropWriteFor = dpWrite(0x00)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	txn := dp.NewTxn()
	abort := txn.WriteDP(dap.ABORT, 1)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want rejected DAPABORT", err)
	}
	if err := abort.Err(); !errors.Is(err, swd.ErrFault) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("DAPABORT result error = %v, want rejected write", err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatalf("ReadWord() after rejected DAPABORT: %v", err)
	}
}

func TestDebugPortTransactionInvalidatesAPForIndeterminateDAPABORT(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	target.waitAfterAbort = true
	txn := dp.NewTxn()
	abort := txn.WriteDP(dap.ABORT, 1)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want WAIT and indeterminate DAPABORT", err)
	}
	if err := abort.Err(); !errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("DAPABORT result error = %v, want indeterminate write", err)
	}
	target.waitAfterAbort = false
	assertMEMAPInvalidated(t, mem)
}

func TestDebugPortTransactionInvalidatesAPWhenDAPABORTBarrierDataParityFails(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	wire := &readParityWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	wire.armed = true
	txn := dp.NewTxn()
	abort := txn.WriteDP(dap.ABORT, 1)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want completed DAPABORT with parity error", err)
	}
	if err := abort.Err(); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("DAPABORT result error = %v, want determinate parity error", err)
	}
	assertMEMAPInvalidated(t, mem)
}

func TestDebugPortTransactionSettlesDPWriteWhenBarrierDataParityFails(t *testing.T) {
	target := newWaitTarget()
	wire := &readParityWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	wire.armed = true
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.SELECT, 0)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want determinate parity error", err)
	}
	if err := write.Err(); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("DP write result error = %v, want determinate parity error", err)
	}

	before := len(target.requests)
	txn = dp.NewTxn()
	read := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, read, 0x2ba01477)
	want := []swdsim.Request{dpRead(0x00)}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("requests after barrier parity error = %#v, want %#v", got, want)
	}
}

func TestDebugPortTransactionTreatsSELECTBarrierDataParityAsDeterminate(t *testing.T) {
	target := newWaitTarget()
	wire := &readParityWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	wire.armed = true
	txn := dp.NewTxn()
	read := txn.ReadDP(dap.DLCR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want determinate SELECT barrier parity error", err)
	}
	if _, err := read.Value(); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("banked DP result error = %v, want determinate parity error", err)
	}

	before := len(target.requests)
	txn = dp.NewTxn()
	dpidr := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, dpidr, 0x2ba01477)
	want := []swdsim.Request{dpRead(0x00)}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("requests after SELECT barrier parity error = %#v, want %#v", got, want)
	}
}

func TestDebugPortTransactionSettlesPreviousDPWriteWhenBarrierDataParityFails(t *testing.T) {
	target := newWaitTarget()
	wire := &readParityWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0); err != nil {
		t.Fatal(err)
	}

	wire.armed = true
	txn := dp.NewTxn()
	read := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want previous write barrier parity error", err)
	}
	if _, err := read.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("DPIDR result error = %v, want not executed", err)
	}

	before := len(target.requests)
	txn = dp.NewTxn()
	read = txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, read, 0x2ba01477)
	want := []swdsim.Request{dpRead(0x00)}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("requests after previous write barrier parity error = %#v, want %#v", got, want)
	}
}

func TestDebugPortImmediateRDBUFFSettlesDPWriteWhenDataParityFails(t *testing.T) {
	target := newWaitTarget()
	wire := &readParityWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0); err != nil {
		t.Fatal(err)
	}

	wire.armed = true
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); !errors.Is(err, swd.ErrParity) {
		t.Fatalf("ReadDP(RDBUFF) error = %v, want parity error", err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	read := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, read, 0x2ba01477)
	want := []swdsim.Request{dpRead(0x00)}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("requests after raw RDBUFF parity error = %#v, want %#v", got, want)
	}
}

func TestDebugPortTransactionCompletesAPWriteWhenBarrierDataParityFails(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	wire := &readParityWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	wire.armed = true
	txn := dp.NewTxn()
	write := txn.WriteRawAP(apSel(0).Address(0x00), 0x23000040)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want determinate parity error", err)
	}
	if err := write.Err(); !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("AP write result error = %v, want determinate parity error", err)
	}
	value, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x00))
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x23000040 {
		t.Fatalf("AP CSW after barrier parity error = %#08x, want applied write", value)
	}
}

func TestDebugPortTransactionAttributesRDBUFFFailure(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	apRead := apRead(0x0c)
	target.armFault(dpRead(0x0c))
	target.waitSkip = 1
	txn := dp.NewTxn()
	faulted := txn.ReadAPIDR(apSel(0))
	suffix := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Commit() error = %v, want FAULT", err)
	}
	if target.executed[apRead] != 1 {
		t.Fatalf("AP read executions = %d, want 1", target.executed[apRead])
	}
	if _, err := faulted.Value(); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("AP result error = %v, want RDBUFF FAULT", err)
	}
	if _, err := faulted.Value(); !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("AP result error = %v, want indeterminate outcome", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func TestDebugPortTransactionMarksAPWriteIndeterminateWhenBarrierFails(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.armFault(dpRead(0x0c))
	txn := dp.NewTxn()
	write := txn.WriteRawAP(apSel(0).Address(0x00), 0x23000040)
	suffix := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want FAULT and indeterminate outcome", err)
	}
	if err := write.Err(); !errors.Is(err, swd.ErrFault) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("AP write result error = %v, want FAULT and indeterminate outcome", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
	value, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x00))
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x23000040 {
		t.Fatalf("AP CSW after failed barrier = %#08x, want applied write", value)
	}
}

func TestDebugPortTransactionMarksAmbiguousOperation(t *testing.T) {
	target := newWaitTarget()
	transferErr := errors.New("injected transaction transport failure")
	wire := &cleanupFailWire{inner: swdsim.New(target), err: transferErr, failBits: 54}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	txn := dp.NewTxn()
	ambiguous := txn.ReadDP(dap.DPIDR)
	suffix := txn.ReadDP(dap.DPIDR)
	wire.armed = true
	if err := txn.Commit(t.Context()); !errors.Is(err, transferErr) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want transport failure and indeterminate outcome", err)
	}
	if _, err := ambiguous.Value(); !errors.Is(err, transferErr) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("ambiguous result error = %v", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func assertTxnValue(t *testing.T, result *dap.ReadResult, want uint32) {
	t.Helper()
	got, err := result.Value()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("transaction result = %#08x, want %#08x", got, want)
	}
}

func assertTxnWrite(t *testing.T, result *dap.WriteResult) {
	t.Helper()
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
}
