package dap_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestMEMAPRejectsDuplicateOwnerBeforeAndAfterRestoration(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0x100: 0x11223344})
	addMEMAP(t, target, 1, 0x24770011, nil)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	for _, restored := range []bool{false, true} {
		if restored {
			if err := first.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		before := len(target.requests)
		if duplicate, err := dap.OpenMemAP(t.Context(), dp, apSel(0)); err == nil || duplicate != nil {
			t.Errorf("restored=%t duplicate owner=%v,%v", restored, duplicate, err)
		}
		if len(target.requests) != before {
			t.Errorf("restored=%t duplicate acquisition sent traffic", restored)
		}
		if got, err := first.ReadWord(t.Context(), 0x100); err != nil || got != 0x11223344 {
			t.Fatalf("original owner reuse=%#x,%v", got, err)
		}
	}
	peer, err := dap.OpenMemAP(t.Context(), dp, apSel(1))
	if err != nil {
		t.Fatalf("distinct AP acquisition: %v", err)
	}
	if err := peer.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestEarlierSessionMEMAPCannotAccessOrRestoreNewSession(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending restoration", true: "restored"}[restored], func(t *testing.T) {
			exerciseEarlierSessionMEMAP(t, restored)
		})
	}
}

func exerciseEarlierSessionMEMAP(t *testing.T, restored bool) {
	t.Helper()
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0x100: 0x11223344})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(4), 0x444); err != nil {
		t.Fatal(err)
	}
	old, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	if restored {
		if err := old.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Change fixture state while the debug-port owner is released.
	if err := target.Target.Write(t.Context(), dpWrite(8), 0); err != nil {
		t.Fatal(err)
	}
	if err := target.Target.Write(t.Context(), apWrite(4), 0x888); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	assertOldSessionClientBlocked(t, old, restored)
	if len(target.requests) != before {
		t.Error("old-session client sent traffic")
	}
	assertAPRegister(t, dp, 4, 0x888)
	fresh, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatalf("new-session owner acquisition: %v", err)
	}
	if err := fresh.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func assertOldSessionClientBlocked(t *testing.T, old *dap.MemAP, restored bool) {
	t.Helper()
	if _, err := old.ReadWord(t.Context(), 0x100); err == nil {
		t.Error("old-session memory read succeeded")
	}
	if err := old.WriteWord(t.Context(), 0x100, 0x55667788); err == nil {
		t.Error("old-session memory write succeeded")
	}
	if _, err := old.ReadBlock(t.Context(), 0x100, make([]byte, 4)); err == nil {
		t.Error("old-session block read succeeded")
	}
	if _, err := old.WriteBlock(t.Context(), 0x100, []byte{1, 2, 3, 4}); err == nil {
		t.Error("old-session block write succeeded")
	}
	if err := old.Release(t.Context()); !restored && err == nil {
		t.Error("old-session pending restoration succeeded")
	}
}

func TestFailedMEMAPAcquisitionDoesNotReserveSelector(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, nil)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.armFault(apRead(4))
	if mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0)); err == nil || mem != nil {
		t.Fatalf("faulted CFG acquisition=%v,%v", mem, err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatalf("retry after failed acquisition: %v", err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMEMAPReservationSurvivesInvalidationAndReleaseRetry(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0x100: 0x11223344})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(4), 0x444); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0)); err != nil {
		t.Fatal(err)
	}
	target.armFault(apWrite(4))
	if err := mem.Release(t.Context()); !errors.Is(err, dap.ErrFault) {
		t.Fatalf("release failure=%v", err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("release retry: %v", err)
	}
	before := len(target.requests)
	if _, err := mem.ReadWord(t.Context(), 0x100); err == nil {
		t.Error("restoration revalidated stale client")
	}
	if next, err := dap.OpenMemAP(t.Context(), dp, apSel(0)); err == nil || next != nil {
		t.Errorf("duplicate after recovery=%v,%v", next, err)
	}
	if len(target.requests) != before {
		t.Error("stale or duplicate handle sent traffic")
	}
	assertAPRegister(t, dp, 4, 0x444)
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMEMAPRestorationRetriesStayInOneSession(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0x100: 0x11223344})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(4), 0x444); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("restore TAR transport failure")
	target.writeErrFor = apWrite(4)
	for range 2 {
		target.writeErr = failure
		if err := mem.Release(t.Context()); !errors.Is(err, failure) {
			t.Fatalf("restore attempt lost its session: %v", err)
		}
	}
	target.writeErr = nil
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("restore after repeated re-entry: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAPRegister(t, dp, 4, 0x444)
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type cancelAtContextCheck struct {
	context.Context
	cancel   context.CancelFunc
	checks   atomic.Int64
	cancelAt int64
}

func (c *cancelAtContextCheck) Err() error {
	if count := c.checks.Add(1); c.cancelAt != 0 && count >= c.cancelAt {
		c.cancel()
	}
	return c.Context.Err()
}

func TestCanceledMEMAPAcquisitionReturnDoesNotKeepReservation(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, nil)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reference := &cancelAtContextCheck{Context: ctx, cancel: cancel}
	mem, err := dap.OpenMemAP(reference, dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	// Locate the final context observation without assuming a fixed number
	// of observations inside the wire and DAP owners.
	checks := reference.checks.Load()
	if checks == 0 {
		t.Fatal("acquisition did not observe its context")
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancelReturn := context.WithCancel(t.Context())
	defer cancelReturn()
	cancelAtReturn := &cancelAtContextCheck{Context: ctx, cancel: cancelReturn, cancelAt: checks}
	if abandoned, err := dap.OpenMemAP(cancelAtReturn, dp, apSel(0)); !errors.Is(err, context.Canceled) || abandoned != nil {
		t.Errorf("canceled acquisition return=%v,%v", abandoned, err)
	}
	fresh, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatalf("retry after canceled acquisition: %v", err)
	}
	if err := fresh.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
