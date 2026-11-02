package jtag

import (
	"bytes"
	"fmt"
	"testing"
)

func TestMovementDoesNotCrossResetUnlessRequested(t *testing.T) {
	for from := Reset; from <= UpdateIR; from++ {
		for to := Reset; to <= UpdateIR; to++ {
			wire := &chainWire{state: from, limit: 2, register: make([]byte, 4), taps: []modelTAP{{ir: 4, instruction: 15}}}
			conn := New(wire)
			conn.state = from
			if err := conn.Move(t.Context(), to); err != nil {
				t.Fatal(err)
			}
			if wire.state != to || conn.State() != to {
				t.Fatalf("movement %v to %v: wire=%v host=%v", from, to, wire.state, conn.State())
			}
			if from != Reset && to != Reset && wire.visits[Reset] != 0 {
				t.Fatalf("movement %v to %v silently reset the chain", from, to)
			}
		}
	}
}

func TestSelectedStateOperationsRetainOrdinaryUpdates(t *testing.T) {
	for _, from := range []State{SelectDR, SelectIR} {
		for _, operation := range []string{"ScanDR", "ScanIR", "Idle", "MeasureIR"} {
			t.Run(fmt.Sprintf("%s/from%d", operation, from), func(t *testing.T) {
				checkSelectedState(t, from, operation)
			})
		}
	}
}

func checkSelectedState(t *testing.T, from State, operation string) {
	t.Helper()
	wire := &chainWire{state: Idle, limit: 2, taps: []modelTAP{{ir: 4, id: 0x14730093, instruction: 15}}}
	conn := New(wire)
	conn.state = Idle
	if err := conn.Move(t.Context(), from); err != nil {
		t.Fatal(err)
	}
	wire.visits = [17]int{}
	wantInstruction := uint64(15)
	want := [17]int{}
	if from == SelectDR {
		want[CaptureDR], want[UpdateDR] = 1, 1
	} else {
		want[CaptureIR], want[UpdateIR], wantInstruction = 1, 1, 1
	}
	switch operation {
	case "ScanDR":
		capture, err := conn.ScanDR(t.Context(), []byte{0}, 1)
		if err != nil || !bytes.Equal(capture, []byte{0}) {
			t.Fatalf("BYPASS capture=%x error=%v", capture, err)
		}
		want[CaptureDR]++
		want[UpdateDR]++
	case "ScanIR":
		capture, err := conn.ScanIR(t.Context(), []byte{15}, 4)
		if err != nil || !bytes.Equal(capture, []byte{1}) {
			t.Fatalf("IR capture=%x error=%v", capture, err)
		}
		want[CaptureIR]++
		want[UpdateIR]++
		wantInstruction = 15
	case "Idle":
		if err := conn.Idle(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
	case "MeasureIR":
		if length, err := conn.MeasureIR(t.Context(), 8); err != nil || length != 4 {
			t.Fatalf("IR length=%d error=%v", length, err)
		}
		want[CaptureIR]++
		want[UpdateIR]++
		wantInstruction = 15
	}
	checkSelectedOutcome(t, wire, conn, want, wantInstruction)
}

func checkSelectedOutcome(t *testing.T, wire *chainWire, conn *Conn, want [17]int, wantInstruction uint64) {
	t.Helper()
	for _, state := range []State{Reset, CaptureDR, UpdateDR, CaptureIR, UpdateIR} {
		if wire.visits[state] != want[state] {
			t.Errorf("state %v clocks=%d want%d", state, wire.visits[state], want[state])
		}
	}
	if wire.state != Idle || conn.State() != Idle || wire.taps[0].instruction != wantInstruction {
		t.Fatalf("final wire=%v host=%v instruction=%x want%x", wire.state, conn.State(), wire.taps[0].instruction, wantInstruction)
	}
}

func TestScansCaptureAgainAfterPartialShift(t *testing.T) {
	for _, from := range []State{ShiftDR, PauseDR, ShiftIR, PauseIR} {
		t.Run(fmt.Sprintf("from%d", from), func(t *testing.T) { checkPartialScan(t, from) })
	}
}

func checkPartialScan(t *testing.T, from State) {
	t.Helper()
	wire := &chainWire{state: Idle, limit: 2, taps: []modelTAP{{ir: 4, id: 0x14730093, instruction: 14}}}
	conn := New(wire)
	conn.state = Idle
	ir := from == ShiftIR || from == PauseIR
	shift := ShiftDR
	if ir {
		shift = ShiftIR
	}
	if err := conn.Move(t.Context(), shift); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.clock(t.Context(), []byte{0}, []byte{0}, 1); err != nil {
		t.Fatal(err)
	}
	if err := conn.Move(t.Context(), from); err != nil {
		t.Fatal(err)
	}
	wire.visits = [17]int{}
	if ir {
		capture, err := conn.ScanIR(t.Context(), []byte{15}, 4)
		if err != nil || !bytes.Equal(capture, []byte{1}) || wire.taps[0].instruction != 15 {
			t.Fatalf("from%v IR capture=%x instruction=%x error=%v", from, capture, wire.taps[0].instruction, err)
		}
	} else {
		capture, err := conn.ScanDR(t.Context(), make([]byte, 4), 32)
		if err != nil || !bytes.Equal(capture, []byte{0x93, 0, 0x73, 0x14}) {
			t.Fatalf("from%v IDCODE capture=%x error=%v", from, capture, err)
		}
	}
	captureState, updateState := CaptureDR, UpdateDR
	if ir {
		captureState, updateState = CaptureIR, UpdateIR
	}
	if wire.visits[captureState] != 1 || wire.visits[updateState] != 2 || wire.visits[Reset] != 0 || conn.State() != Idle {
		t.Fatalf("from%v captures=%d updates=%d resets=%d final=%v", from, wire.visits[captureState], wire.visits[updateState], wire.visits[Reset], conn.State())
	}
}
