package dap_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
)

func TestRegisterAPNativeSWDErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		class error
	}{
		{"WAIT", swd.ErrWait, nil},
		{"FAULT", swd.ErrFault, dap.ErrFault},
		{"protocol", swd.ErrProtocol, dap.ErrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exerciseRegisterAPNativeError(t, tc.cause, tc.class)
		})
	}
}

func exerciseRegisterAPNativeError(t *testing.T, cause, class error) {
	t.Helper()
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model), dap.WithMaxWaits(2))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	model.failFor = apRead(12)
	model.failStatus = dap.RegisterRejected
	model.failCause = cause
	before := len(model.requests)
	id, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
	attempts := 0
	for _, request := range model.requests[before:] {
		if request == apRead(12) {
			attempts++
		}
	}
	if class == nil {
		if err != nil || id.Raw != 0x24770011 || attempts != 2 {
			t.Fatalf("native WAIT retry = %#x,%v, attempts %d", id.Raw, err, attempts)
		}
		return
	}
	if !errors.Is(err, cause) || !errors.Is(err, class) || attempts != 1 {
		t.Fatalf("native error = %v, attempts %d", err, attempts)
	}
	before = len(model.requests)
	_, next := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if class == dap.ErrProtocol {
		if !errors.Is(err, dap.ErrIndeterminate) || next == nil || len(model.requests) != before {
			t.Fatal("native protocol failure left ordinary traffic enabled")
		}
		return
	}
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !fault.StateValid || next != nil {
		t.Fatalf("native FAULT repair = %v, next %v", err, next)
	}
}

func TestRegisterNativeOutcomeContradictions(t *testing.T) {
	for _, cause := range []error{swd.ErrNotExecuted, swd.ErrIndeterminate} {
		t.Run(cause.Error(), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			model.failFor = dpWrite(8)
			model.failStatus = dap.RegisterCompleted
			model.failCause = cause
			err := dp.WriteDP(t.Context(), dap.SELECT, 1)
			if !errors.Is(err, cause) || !errors.Is(err, dap.ErrProtocol) || !errors.Is(err, dap.ErrIndeterminate) {
				t.Fatalf("native outcome contradiction treated as completed: %v", err)
			}
			before := len(model.requests)
			if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || len(model.requests) != before {
				t.Fatal("traffic after inconsistent selection outcome")
			}
		})
	}
}
