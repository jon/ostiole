package dap_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestDPv3TransactionWriteFailures(t *testing.T) {
	for _, mode := range []string{"AP transport", "DP transport", "AP rejected", "selection failed"} {
		t.Run(mode, func(t *testing.T) { checkDPv3TransactionWriteFailures(t, mode) })
	}
}

func checkDPv3TransactionWriteFailures(t *testing.T, mode string) {
	t.Helper()
	target := newWaitTarget()
	target.Target = dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, 20); err != nil {
		t.Fatal(err)
	}
	sel, _ := dap.APAt(0x2000)
	if err := target.AddMEMAP(sel, 0x34770008, nil); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	failure := errors.New("lost response after write")
	target.writeErrFor, target.writeErr = apWrite(4), failure
	if mode == "DP transport" || mode == "selection failed" {
		target.writeErrFor = dpWrite(8)
	}
	if mode == "AP rejected" {
		target.writeErr = nil
		target.armFault(apWrite(4))
	}
	txn := dp.NewTxn()
	prefix := txn.ReadDP(dap.DPIDR)
	var write *dap.WriteResult
	if mode == "DP transport" {
		write = txn.WriteDP(dap.SELECT, 0)
	} else {
		write = txn.WriteRawAP(sel.Address(0xd04), 0x200)
	}
	suffix := txn.ReadDP(dap.DPIDR)
	err := txn.Commit(t.Context())
	wantUncertain := mode == "AP transport" || mode == "DP transport"
	if err == nil || errors.Is(err, dap.ErrIndeterminate) != wantUncertain {
		t.Fatalf("Commit=%v; want uncertainty=%v", err, wantUncertain)
	}
	if errors.Is(write.Err(), dap.ErrIndeterminate) != wantUncertain {
		t.Fatalf("write=%v; want uncertainty=%v", write.Err(), wantUncertain)
	}
	assertTxnValue(t, prefix, 0x4c013477)
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix=%v", err)
	}
}

func TestDPv3BlockWriteKeepsConfirmedPrefix(t *testing.T) {
	target := &observedBlockWriteTarget{waitTarget: newWaitTarget()}
	target.Target = dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, 20); err != nil {
		t.Fatal(err)
	}
	sel, _ := dap.APAt(0x2000)
	if err := target.AddMEMAP(sel, 0x34770008, nil); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	target.failAfter, target.failErr = 3, errors.New("lost write response")
	n, err := mem.WriteBlock(t.Context(), 0x200, make([]byte, 32))
	if n != 8 || !errors.Is(err, dap.ErrIndeterminate) || target.writes != 3 {
		t.Fatalf("write=%d,%v; executions=%d", n, err, target.writes)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestDPv3BlockWriteParityConfirmsWord(t *testing.T) {
	target := &parityAfterDRWTarget{Target: dapsim.New(0x4c013477)}
	if err := target.SetDPRegister(dap.DPIDR1, 20); err != nil {
		t.Fatal(err)
	}
	sel, _ := dap.APAt(0x2000)
	if err := target.AddMEMAP(sel, 0x34770008, nil); err != nil {
		t.Fatal(err)
	}
	target.wire = &readParityWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(target.wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	n, err := mem.WriteBlock(t.Context(), 0x200, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	if n != 4 || !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("write=%d,%v; want one confirmed word and parity", n, err)
	}
	got, err := mem.ReadWord(t.Context(), 0x200)
	if err != nil || got != 0x04030201 {
		t.Fatalf("memory=%#x,%v", got, err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
