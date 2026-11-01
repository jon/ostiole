package dap_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
)

func TestRegisterCompletedSELECTErrorKeepsSelection(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	if err := model.target.SetDPRegister(dap.DLCR, 0x44); err != nil {
		t.Fatal(err)
	}
	model.failFor = dpWrite(8)
	model.failStatus = dap.RegisterCompleted
	model.failCause = context.Canceled
	model.afterWrite = true
	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("completed SELECT=%v", err)
	}
	before := len(model.requests)
	if value, err := dp.ReadDP(t.Context(), dap.DLCR); err != nil || value != 0x44 {
		t.Fatalf("DLCR=%#x,%v", value, err)
	}
	for _, req := range model.requests[before:] {
		if req == dpWrite(8) {
			t.Fatal("completed SELECT replayed")
		}
	}
}

func TestRegisterDPConstructorAndEntryPreflight(t *testing.T) {
	for _, option := range []dap.Option{dap.WithCleanupTimeout(0), dap.WithCleanupTimeout(-time.Second)} {
		model := newRegisterModel(t)
		dp := dap.NewDebugPort(dap.RegisterDP(model), option)
		if _, err := dp.Connect(t.Context()); err == nil || model.connects != 0 || len(model.requests) != 0 || dp.CleanupPending() {
			t.Fatalf("invalid recovery budget=%v connects=%d requests=%d", err, model.connects, len(model.requests))
		}
	}
	dp := dap.NewDebugPort(dap.RegisterDP(nil))
	if _, err := dp.Connect(t.Context()); err == nil || dp.CleanupPending() {
		t.Fatal("nil binding acquired state")
	}
	model := newRegisterModel(t)
	dp = dap.NewDebugPort(dap.RegisterDP(model))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if _, err := dp.Connect(ctx); err == nil || model.connects != 0 || len(model.requests) != 0 {
			t.Fatalf("invalid context=%v", err)
		}
	}
}

func TestRegisterDPRejectedAndUncertainWAITAreDistinct(t *testing.T) {
	for _, status := range []dap.RegisterStatus{dap.RegisterRejected, dap.RegisterIndeterminate} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model), dap.WithMaxWaits(2))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			model.failFor = dpRead(12)
			model.failCause = dap.ErrWait
			model.failStatus = status
			before := len(model.requests)
			_, err := dp.ReadDP(t.Context(), dap.RDBUFF)
			want := 1
			if status == dap.RegisterRejected {
				want = 2
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, dap.ErrIndeterminate) {
				t.Fatalf("uncertain WAIT=%v", err)
			}
			if len(model.requests)-before != want {
				t.Fatalf("attempts=%d want%d", len(model.requests)-before, want)
			}
			if status == dap.RegisterIndeterminate {
				before = len(model.requests)
				if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || len(model.requests) != before {
					t.Fatal("normal traffic after lost completion")
				}
			}
		})
	}
}

func TestRegisterDPCompletedReadKeepsQueuedValueOnCancellation(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	if err := model.target.SetDPRegister(dap.DLCR, 0x44); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	model.failFor = dpRead(4)
	model.failCause = context.Canceled
	model.failStatus = dap.RegisterCompleted
	model.afterRead = true
	model.onCompletion = cancel
	txn := dp.NewTxn()
	first := txn.ReadDP(dap.DLCR)
	suffix := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("commit=%v", err)
	}
	if value, err := first.Value(); value != 0x44 || err != nil {
		t.Fatalf("confirmed read=%#x,%v", value, err)
	}
	if _, err := suffix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("suffix=%v", err)
	}
}

func TestRegisterDPOutcomeValidation(t *testing.T) {
	native := errors.New("register operation failed")
	for _, tc := range []struct {
		status             dap.RegisterStatus
		cause              error
		invalid, uncertain bool
	}{
		{dap.RegisterUnsent, native, false, false},
		{dap.RegisterRejected, native, false, false},
		{dap.RegisterIndeterminate, native, false, true},
		{0, native, true, true},
		{dap.RegisterCompleted, dap.ErrWait, true, true},
		{dap.RegisterUnsent, dap.ErrFault, true, true},
		{dap.RegisterRejected, swd.ErrParity, true, true},
		{dap.RegisterIndeterminate, errors.Join(dap.ErrIndeterminate, dap.ErrNotExecuted), true, true},
	} {
		t.Run(fmt.Sprintf("%d/%v", tc.status, tc.cause), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			model.failFor = dpRead(12)
			model.failCause = tc.cause
			model.failStatus = tc.status
			_, err := dp.ReadDP(t.Context(), dap.RDBUFF)
			if !errors.Is(err, tc.cause) || errors.Is(err, dap.ErrProtocol) != tc.invalid || errors.Is(err, dap.ErrIndeterminate) != tc.uncertain {
				t.Fatalf("outcome=%v invalid=%v uncertain=%v", err, tc.invalid, tc.uncertain)
			}
			before := len(model.requests)
			_, next := dp.ReadDP(t.Context(), dap.CTRLSTAT)
			if tc.uncertain {
				if next == nil || len(model.requests) != before {
					t.Fatalf("ordinary traffic after uncertainty=%v", next)
				}
			} else if next != nil {
				t.Fatalf("rejected/unsent request lost framing: %v", next)
			}
		})
	}
}

func TestRegisterDPReleaseReentryUsesCallerContext(t *testing.T) {
	model := newRegisterModel(t)
	transport := &registerLifecycle{registerModel: model}
	dp := dap.NewDebugPort(dap.RegisterDP(transport))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	model.failFor = dpRead(12)
	model.failStatus = dap.RegisterIndeterminate
	model.failCause = errors.New("response lost")
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("accepted failure=%v", err)
	}
	type contextKey struct{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, contextKey{}, "release")
	deadline, _ := ctx.Deadline()
	transport.enter = func(actual context.Context) (uint32, error) {
		observed, _ := actual.Deadline()
		if actual.Value(contextKey{}) != "release" || !observed.Equal(deadline) {
			t.Errorf("reentry context replaced: value=%v deadline=%v", actual.Value(contextKey{}), observed)
		}
		return model.Connect(actual)
	}
	if err := dp.Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterDPCompletedSelectionDoesNotConfirmUnsentOperation(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			if err := model.target.SetDPRegister(dap.DLCR, 0x44); err != nil {
				t.Fatal(err)
			}
			model.failFor = dpWrite(8)
			model.failStatus = dap.RegisterCompleted
			model.failCause = context.Canceled
			model.afterWrite = true
			txn := dp.NewTxn()
			var outcome func() error
			if write {
				result := txn.WriteDP(dap.DLCR, 0x80)
				outcome = result.Err
			} else {
				result := txn.ReadDP(dap.DLCR)
				outcome = func() error { _, err := result.Value(); return err }
			}
			before := len(model.requests)
			if err := txn.Commit(t.Context()); !errors.Is(err, context.Canceled) {
				t.Fatalf("selection completion=%v", err)
			}
			if err := outcome(); !errors.Is(err, dap.ErrNotExecuted) {
				t.Fatalf("unsent result=%v", err)
			}
			if len(model.requests) != before+1 || model.requests[before] != dpWrite(8) {
				t.Fatal("requested operation was sent after failed selection completion")
			}
			if value, err := dp.ReadDP(t.Context(), dap.DLCR); err != nil || value != 0x44 {
				t.Fatalf("actual selected register=%#x,%v", value, err)
			}
		})
	}
}

func TestRegisterDPAcceptedWriteFaultRetainsUncertainty(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	model.failFor = dpWrite(4)
	model.failStatus = dap.RegisterCompleted
	model.failCause = dap.ErrFault
	model.afterWrite = true
	before := len(model.requests)
	err := dp.WriteDP(t.Context(), dap.CTRLSTAT, 0x50000000)
	if !errors.Is(err, dap.ErrFault) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("accepted write completion fault=%v", err)
	}
	writes := 0
	for _, req := range model.requests[before:] {
		if req == dpWrite(4) {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("accepted write replayed: %d", writes)
	}
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
		t.Fatalf("cleared fault lost DP framing: %v", err)
	}
}

func TestRegisterDPWAITCancellationRetainsNativeCause(t *testing.T) {
	for _, caller := range []bool{false, true} {
		t.Run(fmt.Sprint(caller), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			native := errors.New("register response completion failed")
			want := error(context.DeadlineExceeded)
			model.failFor = dpRead(12)
			model.failStatus = dap.RegisterRejected
			model.failCause = errors.Join(dap.ErrWait, native, context.DeadlineExceeded)
			if caller {
				model.failCause = errors.Join(dap.ErrWait, native)
				model.onCompletion = cancel
				want = context.Canceled
			}
			before := len(model.requests)
			_, err := dp.ReadDP(ctx, dap.RDBUFF)
			if !errors.Is(err, want) || !errors.Is(err, native) || errors.Is(err, dap.ErrWait) {
				t.Fatalf("canceled WAIT=%v", err)
			}
			reads := 0
			for _, req := range model.requests[before:] {
				if req == dpRead(12) {
					reads++
				}
			}
			if reads != 1 {
				t.Fatalf("request replayed after cancellation: %d", reads)
			}
		})
	}
}

func TestRegisterDPCompletedWritesNeedNoExtraBarrier(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	if err := model.target.SetDPRegister(dap.DLCR, 0x44); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
		t.Fatal(err)
	}
	if value, err := dp.ReadDP(t.Context(), dap.DLCR); err != nil || value != 0x44 {
		t.Fatalf("selected register=%#x,%v", value, err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, req := range model.requests {
		if req == dpRead(12) {
			t.Fatal("DAP repeated the completed write's barrier")
		}
	}
}

func TestRegisterDPAcceptedSELECTFaultCannotReadAssumedBankZero(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	if err := model.target.SetDPRegister(dap.DLCR, 0x44); err != nil {
		t.Fatal(err)
	}
	model.failFor = dpWrite(8)
	model.failStatus = dap.RegisterCompleted
	model.failCause = dap.ErrFault
	model.afterWrite = true
	before := len(model.requests)
	err := dp.WriteDP(t.Context(), dap.SELECT, 1)
	var fault *dap.FaultError
	if !errors.Is(err, dap.ErrIndeterminate) || !errors.As(err, &fault) || fault.StateValid {
		t.Fatalf("uncertain SELECT=%v captured=%+v", err, fault)
	}
	if len(model.requests) != before+1 {
		t.Fatal("read an assumed bank-zero register after uncertain SELECT")
	}
	before = len(model.requests)
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || len(model.requests) != before {
		t.Fatal("ordinary traffic after lost selection")
	}
}

func TestRegisterDPWAITKeepsAdditionalFailures(t *testing.T) {
	native := errors.New("response cleanup failed")
	for _, cause := range []error{errors.Join(dap.ErrWait, native), errors.Join(dap.ErrWait, dap.ErrFault)} {
		model := newRegisterModel(t)
		dp := dap.NewDebugPort(dap.RegisterDP(model))
		if _, err := dp.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		releaseErrorPort(t, dp)
		model.failFor = dpRead(12)
		model.failStatus = dap.RegisterRejected
		model.failCause = cause
		before := len(model.requests)
		_, err := dp.ReadDP(t.Context(), dap.RDBUFF)
		if !errors.Is(err, cause) {
			t.Fatalf("lost additional failure: %v", err)
		}
		attempts := 0
		for _, req := range model.requests[before:] {
			if req == dpRead(12) {
				attempts++
			}
		}
		if attempts != 1 {
			t.Fatalf("retried failure: %d requests", attempts)
		}
	}
}

func TestRegisterDPRetriesWrappedSoleWAIT(t *testing.T) {
	for _, cause := range []error{fmt.Errorf("response: %w", dap.ErrWait), errors.Join(dap.ErrWait)} {
		model := newRegisterModel(t)
		dp := dap.NewDebugPort(dap.RegisterDP(model))
		if _, err := dp.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		releaseErrorPort(t, dp)
		model.failFor = dpRead(12)
		model.failStatus = dap.RegisterRejected
		model.failCause = cause
		before := len(model.requests)
		if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); err != nil {
			t.Fatalf("clean WAIT=%v", err)
		}
		if len(model.requests) != before+2 {
			t.Fatalf("sole WAIT attempts=%d", len(model.requests)-before)
		}
	}
}

func TestRegisterDPTypedNilBindingIsRejected(t *testing.T) {
	var transport *registerModel
	dp := dap.NewDebugPort(dap.RegisterDP(transport))
	if _, err := dp.Connect(t.Context()); err == nil || dp.CleanupPending() {
		t.Fatalf("typed nil binding acquired state: %v", err)
	}
}

type functionRegister func()

func (f functionRegister) Connect(context.Context) (uint32, error) {
	f()
	return 0, nil
}
func (f functionRegister) Release(context.Context) error { f(); return nil }
func (f functionRegister) CleanupPending() bool          { f(); return false }
func (f functionRegister) ReadDP(context.Context, uint8) (uint32, dap.RegisterStatus, error) {
	f()
	return 0, dap.RegisterCompleted, nil
}
func (f functionRegister) WriteDP(context.Context, uint8, uint32) (dap.RegisterStatus, error) {
	f()
	return dap.RegisterCompleted, nil
}

func TestRegisterDPNilFunctionAndValueBindings(t *testing.T) {
	var transport functionRegister
	dp := dap.NewDebugPort(dap.RegisterDP(transport))
	if _, err := dp.Connect(t.Context()); err == nil || dp.CleanupPending() {
		t.Fatalf("nil function binding acquired state: %v", err)
	}
	model := newRegisterModel(t)
	dp = dap.NewDebugPort(dap.RegisterDP(struct{ *registerModel }{model}))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if model.connects != 1 {
		t.Fatalf("value binding connects=%d", model.connects)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (f functionRegister) ReadAP(context.Context, uint8) (uint32, dap.RegisterStatus, error) {
	f()
	return 0, dap.RegisterCompleted, nil
}
func (f functionRegister) WriteAP(context.Context, uint8, uint32) (dap.RegisterStatus, error) {
	f()
	return dap.RegisterCompleted, nil
}
