package jtag

import (
	"context"
	"errors"
	"testing"
)

func TestScanBitsAndExit(t *testing.T) {
	for _, limit := range []int{1, 3, 8, 4096} {
		for _, bits := range []int{1, 7, 8, 9, 33} {
			w := &wireFake{limit: limit}
			c := New(w)
			ctx := context.Background()
			if err := c.Reset(ctx); err != nil {
				t.Fatal(err)
			}
			input := make([]byte, (bits+7)/8)
			for i := range input {
				input[i] = 0xa5
			}
			for _, ir := range []bool{true, false} {
				checkScan(t, c, w, input, bits, ir)
			}
		}
	}
}

func checkScan(t *testing.T, c *Conn, w *wireFake, input []byte, bits int, ir bool) {
	t.Helper()
	ctx := context.Background()
	if err := c.Move(ctx, Idle); err != nil {
		t.Fatal(err)
	}
	start := len(w.tms)
	scan, entry := c.ScanDR, 3
	if ir {
		scan, entry = c.ScanIR, 4
	}
	got, err := scan(ctx, input, bits)
	if err != nil {
		t.Fatal(err)
	}
	if c.State() != Idle || len(w.tms)-start != entry+bits+2 {
		t.Fatal("wrong exit or extra clocks")
	}
	for i := range bits {
		if get(got, i) != get(input, i) {
			t.Fatalf("bit %d corrupted", i)
		}
		if w.tms[start+entry+i] != (i == bits-1) {
			t.Fatalf("TMS wrong at scan bit %d", i)
		}
	}
}

func TestScanValidationAndIdle(t *testing.T) {
	w := &wireFake{limit: 8}
	c := New(w)
	ctx := context.Background()
	if err := c.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	before := w.calls
	for _, bits := range []int{-1, 0, 9, MaxScanBits + 1} {
		if _, err := c.ScanDR(ctx, []byte{1}, bits); err == nil {
			t.Fatalf("accepted %d", bits)
		}
	}
	if before != w.calls {
		t.Fatal("invalid scan sent traffic")
	}
	if err := c.Idle(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if c.State() != Idle || len(w.tms) != 13 {
		t.Fatal("idle clocks wrong")
	}
	before = w.calls
	if err := c.Idle(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if w.calls != before {
		t.Fatal("empty idle clocked")
	}
}

func TestScanFailure(t *testing.T) {
	w := &wireFake{limit: 8}
	c := New(w)
	ctx := context.Background()
	if err := c.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	w.fail = w.calls + 2
	if _, err := c.ScanIR(ctx, []byte{0xff}, 8); err == nil {
		t.Fatal("missing failure")
	}
	if _, err := c.ScanDR(ctx, []byte{0}, 1); !errors.Is(err, ErrStateUnknown) {
		t.Fatal(err)
	}
}
