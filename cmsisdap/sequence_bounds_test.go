package cmsisdap

import (
	"reflect"
	"testing"
)

func TestSWDIORejectsUnsafePlanBeforeAnyCommand(t *testing.T) {
	for _, test := range []struct {
		name                       string
		bits, inputStart, inputEnd int
	}{
		{"target owns final bit", 9, 8, 9},
		{"capture exceeds response", 130, 16, 129},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newSWDSequencePeer(16)
			session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			direction := make([]byte, (test.bits+7)/8)
			for bit := range test.bits {
				setTestBit(direction, bit, bit < test.inputStart || bit >= test.inputEnd)
			}
			if _, err := session.SWDIO(t.Context(), direction, make([]byte, len(direction)), test.bits); err == nil {
				t.Error("unsafe plan accepted")
			}
			if len(peer.sequenceCommands) != 0 {
				t.Fatalf("unsafe plan sent %d commands", len(peer.sequenceCommands))
			}
			if _, err := session.SWDIO(t.Context(), []byte{1}, []byte{1}, 1); err != nil {
				t.Fatalf("rejected plan damaged session: %v", err)
			}
		})
	}
}

func TestSWDIOKeepsTargetPhaseWithinOneCommand(t *testing.T) {
	peer := newSWDSequencePeer(16)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	// A short capture leaves too little response space for the 109-bit phase.
	const bits = 112
	direction := make([]byte, (bits+7)/8)
	setTestBit(direction, 1, true)
	setTestBit(direction, 111, true)
	input, err := session.SWDIO(t.Context(), direction, make([]byte, len(direction)), bits)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(peer.direction, testBits(direction, bits)) {
		t.Fatal("direction stream changed")
	}
	if len(input) != len(direction) {
		t.Fatalf("capture length = %d", len(input))
	}
	for _, position := range peer.commandEnds {
		if position > 2 && position < 111 {
			t.Fatalf("firmware enabled output inside target phase at bit %d", position)
		}
	}
	if peer.cycles != bits || !peer.swdioOutput {
		t.Fatalf("final state = %d clocks, output %t", peer.cycles, peer.swdioOutput)
	}
}

func TestSWDIOResponsePacketLimit(t *testing.T) {
	peer := newSWDSequencePeer(16)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	// Fourteen capture bytes exactly fill the response beside its two-byte header.
	const bits = 113
	direction := make([]byte, (bits+7)/8)
	setTestBit(direction, bits-1, true)
	input, err := session.SWDIO(t.Context(), direction, make([]byte, len(direction)), bits)
	if err != nil {
		t.Fatal(err)
	}
	if len(peer.sequenceCommands) != 1 || peer.cycles != bits {
		t.Fatalf("commands = %d, clocks = %d", len(peer.sequenceCommands), peer.cycles)
	}
	for bit := range bits {
		if testBit(input, bit) != (bit < bits-1 && bit%5 == 2) {
			t.Fatalf("wrong capture at bit %d", bit)
		}
	}
}
