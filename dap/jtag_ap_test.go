package dap_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestJTAGAPCompletionDoesNotReplayAcceptedRequests(t *testing.T) {
	m, _, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0), dap.WithMaxWaits(5))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	reads, writes := 0, 0
	m.complete = func(r jtagRequest) uint32 {
		if r.read {
			reads++
		} else {
			writes++
		}
		return 0x24770011
	}
	m.nextWaits = 3
	idr, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
	if err != nil || idr.Raw != 0x24770011 {
		t.Fatalf("APIDR = %#x, %v", idr.Raw, err)
	}
	m.nextWaits = 3
	if err := dp.WriteRawAP(t.Context(), dap.NewAPSel(1).Address(0), 0x1234); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || writes != 1 {
		t.Fatalf("AP effects: %d reads, %d writes", reads, writes)
	}
	accepted := 0
	for _, r := range m.accepted {
		if r.ap {
			accepted++
		}
	}
	if accepted != 2 {
		t.Fatalf("accepted %d AP requests", accepted)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGAPFaultIsCheckedBeforeNextOperation(t *testing.T) {
	m, _, chain := jtagModelChain(t, 8, 1)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	effects := 0
	m.complete = func(jtagRequest) uint32 { effects++; m.ctrl |= 1 << 5; return 0xdeadbeef }
	_, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
	var fault *dap.FaultError
	if !errors.Is(err, dap.ErrFault) || !errors.As(err, &fault) || !fault.StateValid || fault.CTRLSTAT&(1<<5) == 0 {
		t.Fatalf("fault = %v", err)
	}
	if effects != 1 || m.ctrl&(1<<5) != 0 {
		t.Fatalf("effects=%d, CTRL/STAT=%#x", effects, m.ctrl)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGImmediateWriteFaultReportsPossibleEffect(t *testing.T) {
	m, _, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	effects := 0
	m.complete = func(r jtagRequest) uint32 {
		if !r.read {
			effects++
			m.ctrl |= 1 << 5
		}
		return 0
	}
	err := dp.WriteRawAP(t.Context(), dap.NewAPSel(1).Address(12), 42)
	if !errors.Is(err, dap.ErrFault) || !errors.Is(err, dap.ErrIndeterminate) || effects != 1 {
		t.Fatalf("immediate write: effects=%d, error=%v", effects, err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if effects != 1 {
		t.Fatalf("cleanup replayed the write: effects=%d", effects)
	}
}

func TestJTAGPendingAPTimeoutAbortsWithoutReplay(t *testing.T) {
	m, _, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0), dap.WithMaxWaits(2))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	aborts := m.aborts
	m.nextWaits = 10
	_, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
	if !errors.Is(err, dap.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("pending AP timeout = %v", err)
	}
	accepted := 0
	for _, r := range m.accepted {
		if r.ap {
			accepted++
		}
	}
	if accepted != 1 || m.aborts != aborts+1 {
		t.Fatalf("accepted=%d, aborts=%d", accepted, m.aborts-aborts)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGTransactionStopsAtFaultAndKeepsConfirmedPrefix(t *testing.T) {
	m, _, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	effects := 0
	m.complete = func(jtagRequest) uint32 {
		effects++
		if effects == 2 {
			m.ctrl |= 1 << 5
		}
		return uint32(effects)
	}
	txn := dp.NewTxn()
	first := txn.ReadAPIDR(dap.NewAPSel(1))
	second := txn.ReadAPIDR(dap.NewAPSel(1))
	last := txn.WriteRawAP(dap.NewAPSel(1).Address(0), 42)
	if err := txn.Commit(t.Context()); !errors.Is(err, dap.ErrFault) {
		t.Fatalf("Commit = %v", err)
	}
	if value, err := first.Value(); value != 1 || err != nil {
		t.Fatalf("prefix = %d, %v", value, err)
	}
	if _, err := second.Value(); !errors.Is(err, dap.ErrFault) {
		t.Fatalf("fault = %v", err)
	}
	if !errors.Is(last.Err(), dap.ErrNotExecuted) || effects != 2 {
		t.Fatalf("suffix = %v, effects = %d", last.Err(), effects)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
