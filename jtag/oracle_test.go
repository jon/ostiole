package jtag

import "testing"

// modelTransitions follows the TMS arrows in TI SPMA075, Figure 1 (page 3).
// Keep this literal reference independent of the host's transitions and paths.
var modelTransitions = [17][2]State{
	Reset:     {Idle, Reset},
	Idle:      {Idle, SelectDR},
	SelectDR:  {CaptureDR, SelectIR},
	SelectIR:  {CaptureIR, Reset},
	CaptureDR: {ShiftDR, Exit1DR},
	CaptureIR: {ShiftIR, Exit1IR},
	ShiftDR:   {ShiftDR, Exit1DR},
	ShiftIR:   {ShiftIR, Exit1IR},
	Exit1DR:   {PauseDR, UpdateDR},
	Exit1IR:   {PauseIR, UpdateIR},
	PauseDR:   {PauseDR, Exit2DR},
	PauseIR:   {PauseIR, Exit2IR},
	Exit2DR:   {ShiftDR, UpdateDR},
	Exit2IR:   {ShiftIR, UpdateIR},
	UpdateDR:  {Idle, SelectDR},
	UpdateIR:  {Idle, SelectDR},
}

func TestEveryTMSEdgeMatchesIndependentTAP(t *testing.T) {
	for from := Reset; from <= UpdateIR; from++ {
		for tms := byte(0); tms <= 1; tms++ {
			wire := &chainWire{state: from, limit: 8}
			conn := New(wire)
			conn.state = from
			if _, err := conn.clock(t.Context(), []byte{tms}, []byte{0}, 1); err != nil {
				t.Fatal(err)
			}
			want := modelTransitions[from][tms]
			if conn.State() != want || wire.state != want {
				t.Fatalf("edge %v TMS%d: host=%v wire=%v want%v", from, tms, conn.State(), wire.state, want)
			}
		}
	}
}

func TestModelRetainsCaptureShiftAndInstructionUpdate(t *testing.T) {
	const id = uint32(0x14730093)
	wire := &chainWire{state: Reset, limit: 8, taps: []modelTAP{{ir: 4, id: id}}}
	for _, tms := range []byte{0, 1, 0, 0} {
		wire.tick(tms, 0)
	}
	if wire.state != ShiftDR {
		t.Fatalf("ID capture state=%v", wire.state)
	}
	for bit := 0; bit < 32; bit++ {
		tms := byte(0)
		if bit == 31 {
			tms = 1
		}
		if got, want := wire.tick(tms, 0), byte(id>>uint(bit)&1); got != want {
			t.Fatalf("ID bit%d=%d want%d", bit, got, want)
		}
	}
	for _, tms := range []byte{1, 0, 1, 1, 0, 0} {
		wire.tick(tms, 0)
	}
	if wire.state != ShiftIR {
		t.Fatalf("instruction capture state=%v", wire.state)
	}
	for bit := 0; bit < 4; bit++ {
		tms := byte(0)
		if bit == 3 {
			tms = 1
		}
		want := byte(0)
		if bit == 0 {
			want = 1
		}
		if got := wire.tick(tms, 1); got != want {
			t.Fatalf("IR capture bit%d=%d want%d", bit, got, want)
		}
	}
	for _, tms := range []byte{1, 0} {
		wire.tick(tms, 0)
	}
	if wire.taps[0].instruction != 15 {
		t.Fatalf("instruction update=%x wantf", wire.taps[0].instruction)
	}
	for _, tms := range []byte{1, 0, 0} {
		wire.tick(tms, 0)
	}
	if got := wire.tick(1, 1); got != 0 || len(wire.register) != 1 || wire.register[0] != 1 {
		t.Fatalf("BYPASS capture/shift=%d register=%v", got, wire.register)
	}
}
