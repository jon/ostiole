package jtag

import (
	"errors"
	"testing"
)

func TestReconnectReleaseParksAgain(t *testing.T) {
	wire, conn, chain := benchChain(t)
	if err := chain.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := chain.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := chain.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tap, err := chain.TAP(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tap.ScanIR(t.Context(), []byte{14}); err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	if err := chain.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkParking(t, wire, conn, chain, before)
	if _, err := tap.ScanDR(t.Context(), make([]byte, 4), 32); !errors.Is(err, ErrChainInvalid) {
		t.Fatalf("old borrower after release: %v", err)
	}
}

func TestFailedReleaseRetryPerformsParking(t *testing.T) {
	wire, conn, chain := benchChain(t)
	if err := chain.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tap, err := chain.TAP(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tap.ScanIR(t.Context(), []byte{14}); err != nil {
		t.Fatal(err)
	}
	wire.fail = wire.calls + 2
	if err := chain.Release(t.Context()); err == nil || !chain.CleanupPending() {
		t.Fatalf("failed release: error=%v pending=%v", err, chain.CleanupPending())
	}
	wire.fail = 0
	before := wire.calls
	if err := chain.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkParking(t, wire, conn, chain, before)
	before = wire.calls
	if _, err := tap.ScanIR(t.Context(), []byte{14}); !errors.Is(err, ErrChainInvalid) || wire.calls != before {
		t.Fatal("release retry revived an old borrower")
	}
}

func checkParking(t *testing.T, wire *chainWire, conn *Conn, chain *Chain, before int) {
	t.Helper()
	if wire.calls <= before || conn.State() != Idle || wire.state != Idle || chain.CleanupPending() {
		t.Fatalf("parking: calls%d->%d host=%v wire=%v pending=%v", before, wire.calls, conn.State(), wire.state, chain.CleanupPending())
	}
	for index, tap := range wire.taps {
		if tap.instruction != uint64(1)<<uint(tap.ir)-1 {
			t.Fatalf("TAP%d instruction=%x after parking", index, tap.instruction)
		}
	}
}

func TestTAPIdleRetainsInstructionAndUsableBorrow(t *testing.T) {
	wire, _, chain := benchChain(t)
	if err := chain.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tap, err := chain.TAP(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tap.ScanIR(t.Context(), []byte{14}); err != nil {
		t.Fatal(err)
	}
	before := wire.visits
	if err := tap.Idle(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	for state := Reset; state <= UpdateIR; state++ {
		want := before[state]
		if state == Idle {
			want += 5
		}
		if wire.visits[state] != want {
			t.Fatalf("idle clocks in state%v=%d want%d", state, wire.visits[state], want)
		}
	}
	if wire.taps[0].instruction != 14 || wire.taps[1].instruction != 4095 {
		t.Fatal("idle changed the selected instructions")
	}
	capture, err := tap.ScanDR(t.Context(), make([]byte, 4), 32)
	if err != nil || word(capture, 0) != 0x5ba00477 {
		t.Fatalf("scan after idle: %x, %v", capture, err)
	}
}

func TestMeasureIRShortBoundCanReportIncorrectLength(t *testing.T) {
	wire := &chainWire{state: Reset, limit: 3, taps: []modelTAP{{ir: 16, id: 3, irCapture: 0xd801}}}
	conn := New(wire)
	if err := conn.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	if length, err := conn.MeasureIR(t.Context(), 11); err != nil || length != 2 {
		t.Fatalf("short-bound counterexample: length=%d error=%v", length, err)
	}
	if length, err := conn.MeasureIR(t.Context(), 16); err != nil || length != 16 {
		t.Fatalf("sufficient bound: length=%d error=%v", length, err)
	}
}

func TestMeasureIRRejectsStuckLowAndInvalidMarkers(t *testing.T) {
	zero := byte(0)
	wire := &chainWire{state: Reset, limit: 7, taps: []modelTAP{{ir: 4, id: 3}}, stuck: &zero}
	conn := New(wire)
	if err := conn.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	if length, err := conn.MeasureIR(t.Context(), 16); err == nil {
		t.Fatalf("stuck-low TDO accepted as length%d", length)
	}
	for _, input := range [][]byte{{0x7b, 1}, {0xfe, 1}, {0xff, 1}} {
		if length, err := markerLength(input, 8); err == nil {
			t.Fatalf("invalid marker%x accepted as length%d", input, length)
		}
	}
	if length, err := markerLength([]byte{0xef, 1}, 8); err != nil || length != 4 {
		t.Fatalf("valid marker: length%d error%v", length, err)
	}
}
