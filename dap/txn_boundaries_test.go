package dap_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type txnBoundaryWire struct {
	*packedTxnWire
	target *waitTarget
	groups [][]swdsim.Request
}

func (w *txnBoundaryWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	before := len(w.target.requests)
	input, err := w.packedTxnWire.SWDIO(ctx, direction, output, bits)
	w.groups = append(w.groups, append([]swdsim.Request(nil), w.target.requests[before:]...))
	return input, err
}

func newTxnBoundaryPort(t *testing.T, target *waitTarget) (*dap.DebugPort, *txnBoundaryWire) {
	t.Helper()
	wire := &txnBoundaryWire{packedTxnWire: &packedTxnWire{inner: swdsim.New(target), limit: 16_384}, target: target}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dp.Release(ctx); err != nil {
			t.Error(err)
		}
	})
	return dp, wire
}

func assertTxnPhysicalReadBoundaries(t *testing.T, groups [][]swdsim.Request) {
	t.Helper()
	for _, group := range groups {
		for _, req := range group {
			if !req.AP && req.Read && (req.Addr == 0 || req.Addr == 4) && len(group) != 1 {
				t.Errorf("physical sticky-exempt offset can share failed SELECT: %#v", group)
			}
		}
	}
}

func TestTransactionBankedReadSurvivesRejectedSELECT(t *testing.T) {
	for _, reg := range []dap.DPRegister{dap.DLCR, dap.DPIDR1} {
		for _, failure := range []error{swd.ErrWait, swd.ErrFault} {
			t.Run(reg.String()+"/"+failure.Error(), func(t *testing.T) { checkTxnBankedSelectionFailure(t, reg, failure) })
		}
	}
}

func checkTxnBankedSelectionFailure(t *testing.T, reg dap.DPRegister, failure error) {
	t.Helper()
	target := newWaitTarget()
	identity := uint32(0x2ba01477)
	want := uint32(0xa5a50000)
	if reg == dap.DPIDR1 {
		identity = 0x4c013477
		target.Target = dapsim.New(identity)
		want = 20
	}
	if reg == dap.DPIDR1 {
		if err := target.SetDPRegister(reg, want); err != nil {
			t.Fatal(err)
		}
	}
	dp, wire := newTxnBoundaryPort(t, target)
	if reg == dap.DLCR {
		if err := target.SetDPRegister(reg, want); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
		t.Fatal(err)
	}
	if failure == swd.ErrWait {
		target.arm(dpWrite(8), 1)
	} else {
		target.armFault(dpWrite(8))
	}
	wire.calls = nil
	wire.groups = nil
	selectBefore := len(target.selectValues)
	txn := dp.NewTxn()
	prefix := txn.ReadDP(dap.DPIDR)
	result := txn.ReadDP(reg)
	suffix := txn.ReadDP(dap.DPIDR)
	err := txn.Commit(t.Context())
	acceptedSelections := 0
	for _, value := range target.selectValues[selectBefore:] {
		if value == 1 {
			acceptedSelections++
		}
	}
	assertTxnValue(t, prefix, identity)
	if failure == swd.ErrWait {
		if err != nil {
			t.Errorf("recoverable SELECT WAIT: %v", err)
		}
		assertTxnValue(t, result, want)
		assertTxnValue(t, suffix, identity)
		if target.attempts != 2 || acceptedSelections != 1 {
			t.Errorf("SELECT attempts/accepted bank-one writes=%d/%d", target.attempts, acceptedSelections)
		}
	} else {
		assertTxnSelectionFAULT(t, err, result, suffix)
		if target.attempts != 1 || acceptedSelections != 0 {
			t.Errorf("rejected SELECT attempts/accepted bank-one writes=%d/%d", target.attempts, acceptedSelections)
		}
	}
	assertTxnPhysicalReadBoundaries(t, wire.groups)
	if value, err := dp.ReadDP(t.Context(), reg); err != nil || value != want {
		t.Errorf("usable port read=%#x %v", value, err)
	}
}

func assertTxnSelectionFAULT(t *testing.T, err error, result, suffix *dap.ReadResult) {
	t.Helper()
	if !errors.Is(err, dap.ErrFault) || errors.Is(err, dap.ErrIndeterminate) {
		t.Errorf("SELECT FAULT=%v", err)
	}
	if _, err := result.Value(); !errors.Is(err, dap.ErrFault) || errors.Is(err, dap.ErrIndeterminate) {
		t.Errorf("banked result=%v", err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Errorf("unsent suffix=%v", err)
	}
}

func TestTransactionCTRLSTATBoundaryAfterAPWAIT(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp, wire := newTxnBoundaryPort(t, target)
	before, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	target.arm(apRead(12), 1)
	wire.calls = nil
	wire.groups = nil
	txn := dp.NewTxn()
	ap := txn.ReadAPIDR(apSel(0))
	ctrl := txn.ReadDP(dap.CTRLSTAT)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTxnValue(t, ap, 0x24770011)
	assertTxnValue(t, ctrl, before)
	if target.attempts != 2 || target.executed[apRead(12)] != 1 {
		t.Errorf("AP attempts/executions=%d/%d", target.attempts, target.executed[apRead(12)])
	}
	if !slices.Equal(wire.calls, []int{216, 54, 108, 54}) {
		t.Errorf("wire calls=%v", wire.calls)
	}
	assertTxnPhysicalReadBoundaries(t, wire.groups)
}
