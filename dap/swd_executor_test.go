package dap

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/swd"
)

func TestSWDTransferOutcomes(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want transferOutcome
	}{
		{"confirmed", nil, transferConfirmed},
		{"WAIT rejected", swd.ErrWait, transferRejected},
		{"FAULT rejected", swd.ErrFault, transferRejected},
		{"unsent", swd.ErrNotExecuted, transferUnsent},
		{"ambiguous", swd.ErrIndeterminate, transferIndeterminate},
		{"parity", swd.ErrParity, transferConfirmed},
		{"transport", errors.New("transfer failed"), transferIndeterminate},
		{"failed WAIT cleanup", errors.Join(swd.ErrWait, errors.New("cleanup failed")), transferIndeterminate},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := swdResult(42, test.err)
			value, err := result.value()
			if result.outcome != test.want || value != 42 || err != test.err {
				t.Fatalf("result: %+v, value=%d, error=%v", result, value, err)
			}
		})
	}
}
