package swd

import (
	"bytes"
	"testing"
)

func TestSequencePacksDirectionAndOutputBits(t *testing.T) {
	seq := &Sequence{}
	seq.AppendN(3, true, true)
	seq.AppendByte(false, 0xa5)
	seq.Append(false, true)

	if seq.Bits() != 12 {
		t.Fatalf("Bits() = %d", seq.Bits())
	}
	if !bytes.Equal(seq.Direction(), []byte{0x07, 0x00}) {
		t.Fatalf("Direction() = %08b", seq.Direction())
	}
	if !bytes.Equal(seq.Output(), []byte{0x2f, 0x0d}) {
		t.Fatalf("Output() = %08b", seq.Output())
	}
}
