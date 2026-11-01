package dap_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jon/ostiole/dap"
	swdsim "github.com/jon/ostiole/swd/sim"
)

// The target makes data discarded by an accepted write visibly unusable.
// This models the posted-data rule without depending on a buffer's old value.
type discardedPostedTarget struct {
	*waitTarget
	discarded       bool
	simple          bool
	cancelOnDiscard context.CancelFunc
}

func (target *discardedPostedTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if target.simple && req == dpWrite(4) {
		value &^= overrunDetect
	}
	err := target.waitTarget.Write(ctx, req, value)
	if err == nil {
		target.discarded = true
		if target.cancelOnDiscard != nil && req == dpWrite(0) && value == 1<<4 {
			target.cancelOnDiscard()
			target.cancelOnDiscard = nil
		}
	}
	return err
}

func (target *discardedPostedTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := target.waitTarget.Read(ctx, req)
	if err == nil && (req.AP || req == dpRead(12)) {
		if target.discarded {
			value = 0xbad0bad0
		}
		target.discarded = !req.AP
	}
	return value, err
}

func TestPostedReadCompletionWAITDoesNotReturnDiscardedData(t *testing.T) {
	for _, simple := range []bool{false, true} {
		name := "overrun"
		if simple {
			name = "simple"
		}
		t.Run(name, func(t *testing.T) {
			t.Run("immediate", func(t *testing.T) { exercisePostedReadWAIT(t, simple, false) })
			t.Run("transaction", func(t *testing.T) { exercisePostedReadWAIT(t, simple, true) })
		})
	}
}

func exercisePostedReadWAIT(t *testing.T, simple, queued bool) {
	t.Helper()
	target := &discardedPostedTarget{waitTarget: newWaitTarget(), simple: simple}
	addAP(t, target.waitTarget, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatal(err)
	}
	before := target.executed[apRead(12)]
	target.arm(dpRead(12), 2)
	var value uint32
	var err error
	if queued {
		txn := dp.NewTxn()
		result := txn.ReadAPIDR(apSel(0))
		err = txn.Commit(t.Context())
		value, _ = result.Value()
		if !simple && !errors.Is(resultError(result), dap.ErrIndeterminate) {
			t.Errorf("queued result=%v", resultError(result))
		}
	} else {
		var id dap.APIDRInfo
		id, err = dp.ReadAPIDR(t.Context(), apSel(0))
		value = id.Raw
	}
	if simple {
		if err != nil || value != 0x24770011 {
			t.Fatalf("simple completion=%#x,%v", value, err)
		}
	} else if !errors.Is(err, dap.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("discarded completion=%#x,%v; want WAIT and uncertainty", value, err)
	}
	if got := target.executed[apRead(12)] - before; got != 1 {
		t.Fatalf("accepted AP reads=%d", got)
	}
	target.armed = false
}

func resultError(result *dap.ReadResult) error { _, err := result.Value(); return err }

func TestPostedReadWAITRetainsOnlyConfirmedBlockPrefix(t *testing.T) {
	target := &discardedPostedTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target.waitTarget, 0, 0x24770011, map[uint32]uint32{0x100: 0x11, 0x104: 0x22, 0x108: 0x33, 0x10c: 0x44})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := mem.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	target.arm(apRead(12), 2)
	target.waitSkip = 3
	got := make([]byte, 16)
	for i := range got {
		got[i] = 0xff
	}
	n, err := mem.ReadBlock(t.Context(), 0x100, got)
	want := []byte{0x11, 0, 0, 0, 0x22, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if n != 8 || !errors.Is(err, dap.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) || !slices.Equal(got, want) {
		t.Fatalf("ReadBlock=%d,%v,%x; want exact confirmed prefix", n, err, got)
	}
	if got := target.executed[apRead(12)]; got != 3 {
		t.Fatalf("accepted data reads=%d, want3", got)
	}
	target.armed = false
	assertMEMAPInvalidated(t, mem)
}

func TestPostedReadWAITPreservesCancellationAndCleanupFailure(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "immediate"
		if queued {
			name = "transaction"
		}
		t.Run(name, func(t *testing.T) {
			t.Run("cancellation", func(t *testing.T) { exercisePostedReadWAITFailure(t, queued, true) })
			t.Run("cleanup", func(t *testing.T) { exercisePostedReadWAITFailure(t, queued, false) })
		})
	}
}

func exercisePostedReadWAITFailure(t *testing.T, queued, canceled bool) {
	t.Helper()
	target := &discardedPostedTarget{waitTarget: newWaitTarget()}
	addAP(t, target.waitTarget, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		target.armed = false
		target.clearErr = nil
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatal(err)
	}
	before := target.executed[apRead(12)]
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cause := errors.New("sticky clear native failure")
	if canceled {
		cause = context.Canceled
		target.cancelOnDiscard = cancel
	} else {
		target.clearErr = cause
	}
	target.arm(dpRead(12), 2)
	var err error
	if queued {
		txn := dp.NewTxn()
		txn.ReadAPIDR(apSel(0))
		err = txn.Commit(ctx)
	} else {
		_, err = dp.ReadAPIDR(ctx, apSel(0))
	}
	if !errors.Is(err, cause) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("completion failure=%v; want original cause and uncertainty", err)
	}
	if canceled && errors.Is(err, dap.ErrWait) {
		t.Fatalf("cancellation retained WAIT: %v", err)
	}
	if got := target.executed[apRead(12)] - before; got != 1 {
		t.Fatalf("accepted AP reads=%d", got)
	}
}
