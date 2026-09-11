// Package jtag clocks IEEE 1149.1 TAP transitions over a caller-supplied wire.
package jtag

// State identifies a TAP controller state. Unknown is not a destination.
type State uint8

// TAP states follow the IEEE 1149.1 state machine.
const (
	Unknown State = iota
	Reset
	Idle
	SelectDR
	CaptureDR
	ShiftDR
	Exit1DR
	PauseDR
	Exit2DR
	UpdateDR
	SelectIR
	CaptureIR
	ShiftIR
	Exit1IR
	PauseIR
	Exit2IR
	UpdateIR
)

var transitions = [17][2]State{
	Reset: {Idle, Reset}, Idle: {Idle, SelectDR},
	SelectDR: {CaptureDR, SelectIR}, CaptureDR: {ShiftDR, Exit1DR},
	ShiftDR: {ShiftDR, Exit1DR}, Exit1DR: {PauseDR, UpdateDR},
	PauseDR: {PauseDR, Exit2DR}, Exit2DR: {ShiftDR, UpdateDR},
	UpdateDR: {Idle, SelectDR}, SelectIR: {CaptureIR, Reset},
	CaptureIR: {ShiftIR, Exit1IR}, ShiftIR: {ShiftIR, Exit1IR},
	Exit1IR: {PauseIR, UpdateIR}, PauseIR: {PauseIR, Exit2IR},
	Exit2IR: {ShiftIR, UpdateIR}, UpdateIR: {Idle, SelectDR},
}

func path(from, to State) []bool {
	type route struct {
		state State
		bits  []bool
	}
	queue := []route{{state: from}}
	seen := [17]bool{}
	seen[from] = true
	for len(queue) != 0 {
		r := queue[0]
		queue = queue[1:]
		if r.state == to {
			return r.bits
		}
		for bit, next := range transitions[r.state] {
			if !seen[next] {
				seen[next] = true
				bits := append(append([]bool(nil), r.bits...), bit != 0)
				queue = append(queue, route{next, bits})
			}
		}
	}
	return nil
}
