package dap

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/swd"
)

type inertEntryWire struct{ calls int }

func (w *inertEntryWire) SWDIO(context.Context, []byte, []byte, int) ([]byte, error) {
	w.calls++
	return nil, errors.New("unexpected wire traffic")
}

func TestUnsentSWDRequestPreservesDAPObservations(t *testing.T) {
	wire := &inertEntryWire{}
	dp := NewDebugPort(SWDP(swd.New(wire)))
	dp.state.session = sessionConnected
	dp.state.response = responseOverrun
	dp.state.selectDP = selectState{value: 0, valid: true}
	before := dp.state
	_, err := dp.conn.transfer(t.Context(), dpTransferRequest(DPIDR, true), 0)
	if !errors.Is(err, swd.ErrNotExecuted) || !requestWasNotSent(err) {
		t.Fatalf("lost unsent classification: %v", err)
	}
	if wire.calls != 0 || dp.state != before {
		t.Fatal("unsent request changed wire or DAP state")
	}
}
