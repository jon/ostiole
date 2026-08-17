package swd_test

import (
	"context"
	"testing"

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
	wire := &countingWire{}
	conn := swd.New(wire)
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
	if wire.calls != 0 {
		t.Fatalf("invalid addresses made %d wire calls", wire.calls)
	}
}
