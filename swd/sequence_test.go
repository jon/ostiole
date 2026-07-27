package swd

import (
	"bytes"
	"testing"
)

func TestSequencePacksDirectionAndOutputBits(t *testing.T) {
	seq := &sequence{}
	seq.appendN(3, true, true)
	seq.appendByte(false, 0xa5)
	seq.append(false, true)

	if seq.bits != 12 {
		t.Fatalf("bits = %d", seq.bits)
	}
	if !bytes.Equal(seq.direction, []byte{0x07, 0x00}) {
		t.Fatalf("direction = %08b", seq.direction)
	}
	if !bytes.Equal(seq.output, []byte{0x2f, 0x0d}) {
		t.Fatalf("output = %08b", seq.output)
	}
}
