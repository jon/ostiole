package jtag

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type unlimitedWire struct{ wire *wireFake }

func (w unlimitedWire) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	return w.wire.JTAGIO(ctx, tms, tdi, bits)
}

func TestAdvertisedWireLimitAndFallback(t *testing.T) {
	for _, limit := range []int{512, 8192, 0} {
		wire := &wireFake{limit: 16384}
		var transport Wire = wire
		want := limit
		if limit == 0 {
			transport, want = unlimitedWire{wire}, 4096
		} else {
			wire.limit = limit
		}
		conn := New(transport)
		if err := conn.Reset(t.Context()); err != nil {
			t.Fatal(err)
		}
		wire.sizes = nil
		if _, err := conn.ScanDR(t.Context(), make([]byte, 1025), 8193); err != nil {
			t.Fatal(err)
		}
		if got := slices.Max(wire.sizes); got != want {
			t.Errorf("advertised limit%d: largest call%d want%d", limit, got, want)
		}
	}
}

func TestKnownStateRejectsInvalidDestinationsWithoutTraffic(t *testing.T) {
	wire := &wireFake{limit: 8}
	conn := New(wire)
	if err := conn.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	for _, destination := range []State{Unknown, UpdateIR + 1, 255} {
		if err := conn.Move(t.Context(), destination); err == nil || errors.Is(err, ErrStateUnknown) {
			t.Fatalf("destination%d: %v", destination, err)
		}
		if wire.calls != before || conn.State() != Reset {
			t.Fatal("invalid destination changed a known TAP")
		}
	}
}

func TestLayoutCountBoundUsesValidSpecifications(t *testing.T) {
	good, err := IDCODE(4, 0x5ba00477)
	if err != nil {
		t.Fatal(err)
	}
	layout := make(Layout, 1025)
	for i := range layout {
		layout[i] = good
	}
	if err := layout[:1024].Validate(); err != nil {
		t.Fatalf("maximum valid layout: %v", err)
	}
	wire := &wireFake{limit: 8}
	if err := layout.Validate(); err == nil {
		t.Fatal("1025 valid specifications accepted")
	}
	if _, err := NewChain(New(wire), layout); err == nil || wire.calls != 0 {
		t.Fatalf("oversized layout preflight: error=%v calls=%d", err, wire.calls)
	}
}

func TestShortPhysicalChainRejectsBeforeBorrowing(t *testing.T) {
	wire, _, chain := benchChain(t)
	wire.taps = wire.taps[:1]
	if err := chain.Connect(t.Context()); err == nil || !strings.Contains(err.Error(), "TAP count mismatch") {
		t.Fatalf("short physical chain: %v", err)
	}
	if _, err := chain.TAP(0); !errors.Is(err, ErrChainInvalid) {
		t.Fatalf("borrow after mismatch: %v", err)
	}
}

func TestIRCaptureRequiresZeroAtEverySecondBit(t *testing.T) {
	for position := range 2 {
		wire, _, chain := benchChain(t)
		wire.taps[position].captureBitOne = true
		if err := chain.Connect(t.Context()); err == nil || !strings.Contains(err.Error(), "IR capture mismatch") {
			t.Fatalf("capture11 at TAP%d: %v", position, err)
		}
		if _, err := chain.TAP(position); !errors.Is(err, ErrChainInvalid) {
			t.Fatalf("borrow after bad capture: %v", err)
		}
	}
}

func TestInstructionBuffersRejectBeforeTraffic(t *testing.T) {
	wire, _, chain := benchChain(t)
	if err := chain.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tap, err := chain.TAP(0)
	if err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	for _, instruction := range [][]byte{nil, {}, {15, 0}, {15, 0, 0}, {0x10}} {
		if _, err := tap.ScanIR(t.Context(), instruction); err == nil {
			t.Fatalf("instruction buffer%x accepted", instruction)
		}
		if wire.calls != before {
			t.Fatal("invalid instruction reached wire")
		}
		if _, err := chain.TAP(0); err != nil {
			t.Fatalf("invalid instruction invalidated chain: %v", err)
		}
	}
	if _, err := tap.ScanIR(t.Context(), []byte{15}); err != nil {
		t.Fatal(err)
	}
}
