package swd_test

import (
	"context"
	"testing"
	"time"

	dapsim "github.com/jon/ostiole/dap/sim"
	swdsim "github.com/jon/ostiole/swd/sim"

	"github.com/jon/ostiole/swd"
)

type countingWire struct {
	calls int
}

func (w *countingWire) SWDIO(context.Context, []byte, []byte, int) ([]byte, error) {
	w.calls++
	return nil, nil
}

func TestTransferMethodsRejectInvalidAddressesBeforeTraffic(t *testing.T) {
	wire := &entryFailureWire{inner: swdsim.New(dapsim.New(0x2ba01477))}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	if _, err := conn.ReadDP(t.Context(), 0x01); err == nil {
		t.Fatal("ReadDP() with address 0x01 succeeded")
	}
	if err := conn.WriteDP(t.Context(), 0x02, 0); err == nil {
		t.Fatal("WriteDP() with address 0x02 succeeded")
	}
	if _, err := conn.ReadAP(t.Context(), 0x03); err == nil {
		t.Fatal("ReadAP() with address 0x03 succeeded")
	}
	if err := conn.WriteAP(t.Context(), 0x10, 0); err == nil {
		t.Fatal("WriteAP() with address 0x10 succeeded")
	}
	if wire.calls != before {
		t.Fatalf("invalid addresses made %d wire calls", wire.calls-before)
	}
	if _, err := conn.ReadDP(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidCompletionBudgetSendsNoTraffic(t *testing.T) {
	for _, budget := range []time.Duration{0, -time.Second} {
		wire := &countingWire{}
		conn := swd.New(wire, swd.WithCompletionTimeout(budget))
		if _, err := conn.Connect(t.Context()); err == nil || wire.calls != 0 {
			t.Fatalf("invalid completion budget reached wire: %v calls%d", err, wire.calls)
		}
	}
}
