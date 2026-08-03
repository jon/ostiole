package swd_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/jon/ostiole/swd"
)

type recordedWire struct {
	direction []byte
	output    []byte
	bits      int
}

func (w *recordedWire) SWDIO(_ context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.direction = append([]byte(nil), direction...)
	w.output = append([]byte(nil), output...)
	w.bits = bits
	return make([]byte, (bits+7)/8), nil
}

func TestJTAGToSWDSelectsTheSWDInterface(t *testing.T) {
	w := &recordedWire{}
	if err := swd.New(w).JTAGToSWD(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := append(bytes.Repeat([]byte{0xff}, 7), 0x9e, 0xe7)
	want = append(want, bytes.Repeat([]byte{0xff}, 7)...)
	want = append(want, 0)
	if w.bits != 136 ||
		!bytes.Equal(w.direction, bytes.Repeat([]byte{0xff}, 17)) ||
		!bytes.Equal(w.output, want) {
		t.Fatalf("JTAGToSWD() = %d bits, direction % x, output % x", w.bits, w.direction, w.output)
	}
}

func TestLineResetEndsWithIdleCycles(t *testing.T) {
	w := &recordedWire{}
	if err := swd.New(w).LineReset(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0,
	}
	if w.bits != 64 ||
		!bytes.Equal(w.direction, bytes.Repeat([]byte{0xff}, 8)) ||
		!bytes.Equal(w.output, want) {
		t.Fatalf("LineReset() = %d bits, direction % x, output % x", w.bits, w.direction, w.output)
	}
}
