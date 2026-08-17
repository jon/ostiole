package dap_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
)

func TestReadAlignedMEMAPWord(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{
		0xe000ed00: 0x410cc200,
	})
	dp := enteredDAPClient(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 0xa5000051); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	value, err := mem.ReadWord(t.Context(), 0xe000ed00)
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x410cc200 {
		t.Fatalf("ReadWord() = %#08x, want 0x410cc200", value)
	}
	csw, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0))
	if err != nil {
		t.Fatal(err)
	}
	if csw != 0xa5000042 {
		t.Fatalf("CSW = %#08x, want preserved value 0xa5000042", csw)
	}
}

func TestMEMAPReleaseRestoresRegisterState(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{
		0xe000ed00: 0x410cc200,
	})
	dp := enteredDAPClient(t, target)
	const (
		originalCSW = uint32(0xa5000051)
		originalTAR = uint32(0x20000000)
	)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), originalCSW); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x04), originalTAR); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatal(err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAPRegister(t, dp, 0x00, originalCSW)
	assertAPRegister(t, dp, 0x04, originalTAR)
}

func TestMEMAPReleaseCanRetry(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := mem.Release(canceled); err == nil {
		t.Fatal("Release() succeeded with a canceled context")
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("retry Release(): %v", err)
	}
}

func TestMEMAPRejectsUnalignedWord(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, nil)
	mem, err := dap.NewMemAP(t.Context(), enteredDAPClient(t, target), apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 3); err == nil {
		t.Fatal("ReadWord() succeeded with an unaligned address")
	}
}

func TestImmediateRawAPWriteInvalidatesMEMAP(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); err != nil {
		t.Fatal(err)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestQueuedRawAPWriteInvalidatesMEMAP(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	write := txn.WriteRawAP(apSel(0).Address(0), 2)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := write.Value(); err != nil {
		t.Fatal(err)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestImmediateRawAPReadInvalidatesMEMAP(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 2); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x0c)); err != nil {
		t.Fatal(err)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestQueuedRawAPReadInvalidatesMEMAP(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 2); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	read := txn.ReadRawAP(apSel(0).Address(0x0c))
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := read.Value(); err != nil {
		t.Fatal(err)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestAPIDRReadsKeepMEMAPUsable(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	read := txn.ReadAPIDR(apSel(0))
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := read.Value(); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after APIDR reads: %v", err)
	}
}

func TestIndeterminateImmediateRawAPWriteInvalidatesMEMAP(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); !errors.Is(err, errBarrier) {
		t.Fatalf("WriteRawAP() error = %v, want %v", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestIndeterminateQueuedRawAPWriteInvalidatesMEMAP(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	txn := dp.NewTxn()
	write := txn.WriteRawAP(apSel(0).Address(0), 2)
	if err := txn.Commit(t.Context()); !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want %v and indeterminate", err, errBarrier)
	}
	if _, err := write.Value(); !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("write result error = %v, want %v and indeterminate", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestIndeterminateImmediateRawAPReadInvalidatesMEMAP(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x00)); !errors.Is(err, errBarrier) {
		t.Fatalf("ReadRawAP() error = %v, want %v", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestIndeterminateQueuedRawAPReadInvalidatesMEMAP(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	txn := dp.NewTxn()
	read := txn.ReadRawAP(apSel(0).Address(0x00))
	if err := txn.Commit(t.Context()); !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want %v and indeterminate", err, errBarrier)
	}
	if _, err := read.Value(); !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("read result error = %v, want %v and indeterminate", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestInvalidRawAPReadKeepsMEMAPUsable(t *testing.T) {
	target := newWaitTarget()
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x02)); err == nil {
		t.Fatal("ReadRawAP() accepted unaligned address 0x02")
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after invalid raw read: %v", err)
	}
}

func TestCanceledRawAPReadKeepsMEMAPUsableWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(target.requests)
	if _, err := dp.ReadRawAP(ctx, apSel(0).Address(0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadRawAP() error = %v, want context canceled", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("canceled raw read sent %d requests", got-before)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after canceled raw read: %v", err)
	}
}

func TestCanceledQueuedRawAPReadKeepsMEMAPUsableWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	read := txn.ReadRawAP(apSel(0).Address(0))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(target.requests)
	if err := txn.Commit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit() error = %v, want context canceled", err)
	}
	if _, err := read.Value(); !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("read result error = %v, want determinate context cancellation", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("canceled queued raw read sent %d requests", got-before)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after canceled queued raw read: %v", err)
	}
}

func TestRejectedRawAPWritesKeepMEMAPUsable(t *testing.T) {
	target := newWaitTarget()
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x02), 2); err == nil {
		t.Fatal("WriteRawAP() accepted unaligned address 0x02")
	}
	target.armFault(apWrite(0x00))
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("WriteRawAP() error = %v, want %v", err, swd.ErrFault)
	}
	value, err := mem.ReadWord(t.Context(), 0)
	if err != nil {
		t.Fatalf("ReadWord() after rejected raw writes: %v", err)
	}
	if value != 1 {
		t.Fatalf("ReadWord() = %#08x, want 1", value)
	}
	target.dropWriteFor = apWrite(0x00)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("WriteRawAP() after abandoned data error = %v, want %v", err, swd.ErrFault)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after WDATAERR: %v", err)
	}
}

func TestCanceledRawAPWriteKeepsMEMAPUsableWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.NewMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(target.requests)
	if err := dp.WriteRawAP(ctx, apSel(0).Address(0), 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteRawAP() error = %v, want context canceled", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("canceled raw write sent %d requests", got-before)
	}
	value, err := mem.ReadWord(t.Context(), 0)
	if err != nil {
		t.Fatalf("ReadWord() after canceled raw write: %v", err)
	}
	if value != 1 {
		t.Fatalf("ReadWord() = %#08x, want 1", value)
	}
}

func TestNewMEMAPRejectsAbsentAndNonMemoryPorts(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddAP(1, 0x00000001)
	dp := enteredDAPClient(t, target)
	var zero dap.APSel
	if _, err := dap.NewMemAP(t.Context(), dp, zero); err == nil {
		t.Fatal("NewMemAP() accepted a zero APSel")
	}
	if _, err := dap.NewMemAP(t.Context(), dp, apSel(0)); err == nil {
		t.Fatal("NewMemAP() accepted an absent AP")
	}
	if _, err := dap.NewMemAP(t.Context(), dp, apSel(1)); err == nil {
		t.Fatal("NewMemAP() accepted a non-MEM AP")
	}
}

func TestNilMEMAPClient(t *testing.T) {
	if _, err := dap.NewMemAP(t.Context(), nil, apSel(0)); err == nil {
		t.Fatal("NewMemAP() succeeded without a debug port")
	}
	var mem *dap.MemAP
	if _, err := mem.ReadWord(t.Context(), 0); err == nil {
		t.Fatal("ReadWord() succeeded on a nil MEM-AP")
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("nil Release(): %v", err)
	}
}

func assertAPRegister(t *testing.T, dp *dap.DebugPort, address uint8, want uint32) {
	t.Helper()
	got, err := dp.ReadRawAP(t.Context(), apSel(0).Address(address))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("AP register %#02x = %#08x, want %#08x", address, got, want)
	}
}

func assertMEMAPHandleInvalid(t *testing.T, mem *dap.MemAP) {
	t.Helper()
	if _, err := mem.ReadWord(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("ReadWord() error = %v, want invalidated MEM-AP", err)
	}
}

func enteredDAPClient(t *testing.T, target *sim.Target) *dap.DebugPort {
	t.Helper()
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})
	return dp
}
