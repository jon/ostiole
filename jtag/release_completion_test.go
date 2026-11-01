package jtag

import (
	"context"
	"errors"
	"testing"
)

type cancelCompletedScanWire struct {
	Wire
	cancel context.CancelFunc
}

func (w *cancelCompletedScanWire) MaxTransferBits() int {
	return w.Wire.(TransferLimits).MaxTransferBits()
}

func (w *cancelCompletedScanWire) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	out, err := w.Wire.JTAGIO(ctx, tms, tdi, bits)
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	return out, err
}

func TestCanceledCompletedChainReleaseDoesNotRepeat(t *testing.T) {
	wire, conn, chain := benchChain(t)
	if err := chain.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conn.wire = &cancelCompletedScanWire{Wire: wire, cancel: cancel}
	if err := chain.Release(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, tap := range wire.taps {
		if tap.instruction != uint64(1)<<uint(tap.ir)-1 {
			t.Fatalf("parking did not complete: %#x", tap.instruction)
		}
	}
	if chain.CleanupPending() {
		t.Fatal("completed parking retained ownership")
	}
	before := wire.calls
	if err := chain.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatalf("completed parking replayed: %v, calls%d -> %d", err, before, wire.calls)
	}
}

func TestUnstartedChainReleaseSendsNoTraffic(t *testing.T) {
	wire, _, chain := benchChain(t)
	if chain.CleanupPending() {
		t.Fatal("constructor acquired cleanup")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := chain.Connect(ctx); !errors.Is(err, context.Canceled) || chain.CleanupPending() {
		t.Fatalf("unstarted entry acquired cleanup: %v", err)
	}
	if err := chain.Release(t.Context()); err != nil || wire.calls != 0 {
		t.Fatalf("unstarted release acquired chain: %v calls%d", err, wire.calls)
	}
}
