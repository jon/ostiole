package dap

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/swd"
)

func TestClassifyPortError(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"success", nil, nil},
		{"context", context.Canceled, context.Canceled},
		{"WAIT", swd.ErrWait, ErrWait},
		{"FAULT", swd.ErrFault, ErrFault},
		{"protocol", swd.ErrProtocol, ErrProtocol},
		{"wrapped", fmt.Errorf("transfer: %w", swd.ErrWait), ErrWait},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := classifyPortError(test.err)
			if !errors.Is(err, test.want) || !errors.Is(err, test.err) {
				t.Fatalf("error = %v; want %v and %v", err, test.want, test.err)
			}
			if err != nil && err.Error() != test.err.Error() {
				t.Fatalf("error text changed: %v", err)
			}
		})
	}
}

func TestClassifyPortErrorRetainsJoinedCauses(t *testing.T) {
	fault := &FaultError{StateValid: true, CTRLSTAT: stickyError, cause: swd.ErrFault}
	err := classifyPortError(errors.Join(fault, swd.ErrProtocol, context.Canceled))
	for _, want := range []error{ErrFault, ErrProtocol, swd.ErrFault, swd.ErrProtocol, context.Canceled} {
		if !errors.Is(err, want) {
			t.Errorf("error = %v; want %v", err, want)
		}
	}
	var captured *FaultError
	if !errors.As(err, &captured) || captured != fault {
		t.Fatalf("fault = %v; want original %v", captured, fault)
	}
}
