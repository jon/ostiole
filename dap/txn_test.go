package dap_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestDebugPortTransactionResolvesOrderedOperations(t *testing.T) {
	target := newWaitTarget()
	target.AddAP(0, 0x24770011)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	txn := dp.NewTxn()
	dpidr := txn.ReadDP(dap.DPIDRAddr)
	idr := txn.ReadAP(0, dap.APIDR)
	write := txn.WriteAP(0, dap.APCSW, 0x23000040)
	csw := txn.ReadAP(0, dap.APCSW)
	if _, err := dpidr.Value(); !errors.Is(err, dap.ErrResultPending) {
		t.Fatalf("Value() before Commit error = %v, want pending", err)
	}
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, dpidr, 0x2ba01477)
	assertTxnValue(t, idr, 0x24770011)
	assertTxnValue(t, write, 0)
	assertTxnValue(t, csw, 0x23000040)
	if err := txn.Commit(t.Context()); !errors.Is(err, dap.ErrTxnCommitted) {
		t.Fatalf("second Commit() error = %v, want committed", err)
	}
	if _, err := txn.ReadDP(dap.DPIDRAddr).Value(); !errors.Is(err, dap.ErrTxnCommitted) {
		t.Fatalf("queue after Commit error = %v, want committed", err)
	}
}

func TestDebugPortTransactionValidatesBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	target.AddAP(0, 0x24770011)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	valid := txn.ReadAP(0, dap.APIDR)
	invalid := txn.ReadAP(0, dap.APReg(2))
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded with an unaligned AP register")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after failed validation = %d, want %d", got, before)
	}
	if _, err := valid.Value(); err != dap.ErrNotExecuted {
		t.Fatalf("valid result error = %v, want not executed", err)
	}
	if _, err := invalid.Value(); err == nil || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("invalid result error = %v, want validation error", err)
	}
}

func TestDebugPortTransactionAttributesAccessPreflightFailure(t *testing.T) {
	target := newWaitTarget()
	target.AddAP(0, 0x24770011)
	dp := enteredDP(t, target)

	before := len(target.requests)
	txn := dp.NewTxn()
	prefix := txn.ReadDP(dap.DPIDRAddr)
	inaccessible := txn.ReadAP(0, dap.APIDR)
	suffix := txn.ReadDP(dap.DPIDRAddr)
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded without a connected debug port")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after failed access preflight = %d, want %d", got, before)
	}
	if _, err := prefix.Value(); err != dap.ErrNotExecuted {
		t.Fatalf("prefix result error = %v, want not executed", err)
	}
	if _, err := inaccessible.Value(); err == nil || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("inaccessible AP result error = %v, want connection error", err)
	}
	if _, err := suffix.Value(); err != dap.ErrNotExecuted {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func TestDebugPortTransactionRejectsReadOnlyDPWriteBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	target.dpidrOverride = 0x2ba02477
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, addr := range []dap.DPAddress{dap.RDBUFFAddr, dap.TARGETIDAddr} {
		before := len(target.requests)
		txn := dp.NewTxn()
		result := txn.WriteDP(addr, 0)
		if err := txn.Commit(t.Context()); err == nil {
			t.Fatalf("Commit() succeeded with read-only DP write %+v", addr)
		}
		if got := len(target.requests); got != before {
			t.Fatalf("requests after rejected DP write = %d, want %d", got, before)
		}
		if _, err := result.Value(); err == nil {
			t.Fatalf("DP write result for %+v succeeded", addr)
		}
	}
}

func TestDebugPortTransactionRejectsUnsupportedFramingBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		addr  dap.DPAddress
		value uint32
	}{
		{name: "overrun detection", addr: dap.CTRLSTATAddr, value: overrunDetect},
		{name: "turnaround", addr: dap.DLCRAddr, value: 1 << 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(target.requests)
			txn := dp.NewTxn()
			read := txn.ReadDP(dap.DPIDRAddr)
			write := txn.WriteDP(test.addr, test.value)
			if err := txn.Commit(t.Context()); err == nil {
				t.Fatal("Commit() accepted unsupported response framing")
			}
			if got := len(target.requests); got != before {
				t.Fatalf("requests after rejected DP write = %d, want %d", got, before)
			}
			if _, err := read.Value(); err != dap.ErrNotExecuted {
				t.Fatalf("DPIDR result error = %v, want not executed", err)
			}
			if _, err := write.Value(); err == nil {
				t.Fatal("DP write result succeeded")
			}
		})
	}
}

func TestDebugPortTransactionRejectsDPv3BankWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	target.dpidrOverride = 0x2ba03477
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	result := txn.ReadDP(dap.DLCRAddr)
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
	target.AddAP(0, 0x24770011)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.armFault(swd.Request{AP: true, Read: true, Addr: 0x0c})
	txn := dp.NewTxn()
	prefix := txn.ReadDP(dap.DPIDRAddr)
	faulted := txn.ReadAP(0, dap.APIDR)
	suffix := txn.ReadDP(dap.DPIDRAddr)
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
	target.AddAP(0, 0x24770011)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.dropWriteFor = swd.Request{Addr: uint8(dap.SELECT)}
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.SELECTAddr, 0x12000000)
	suffix := txn.ReadAP(0, dap.APIDR)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Commit() error = %v, want FAULT", err)
	}
	if _, err := write.Value(); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("DP write result error = %v, want FAULT", err)
	}
	if _, err := write.Value(); errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("DP write result error = %v, want known abandoned write", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix result error = %v, want not executed", err)
	}
}

func TestDebugPortTransactionSettlesDPWriteThroughRDBUFF(t *testing.T) {
	target := newWaitTarget()
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.SELECTAddr, 0)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, write, 0)
	want := []swd.Request{{Addr: uint8(dap.SELECT)}, {Read: true, Addr: uint8(dap.RDBUFF)}}
	if got := target.requests[before:]; !equalRequests(got, want) {
		t.Fatalf("DP write requests = %#v, want %#v", got, want)
	}
}

func TestDebugPortTransactionMarksAmbiguousOperation(t *testing.T) {
	target := newWaitTarget()
	transferErr := errors.New("injected transaction transport failure")
	wire := &cleanupFailWire{inner: swdsim.New(target), err: transferErr, failBits: 42}
	conn := swd.New(wire)
	if err := conn.JTAGToSWD(t.Context()); err != nil {
		t.Fatal(err)
	}
	dp := dap.NewSWDP(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	txn := dp.NewTxn()
	ambiguous := txn.ReadDP(dap.DPIDRAddr)
	suffix := txn.ReadDP(dap.DPIDRAddr)
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

func assertTxnValue(t *testing.T, result *dap.Result, want uint32) {
	t.Helper()
	got, err := result.Value()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("transaction result = %#08x, want %#08x", got, want)
	}
}
