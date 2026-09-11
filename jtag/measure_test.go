package jtag

import (
	"context"
	"testing"
)

func TestMeasureIR(t *testing.T) {
	for _, limit := range []int{1, 7, 4096} {
		w := &chainWire{state: Idle, limit: limit, taps: []modelTAP{{ir: 4, id: 3}, {ir: 12, id: 5}}}
		c := New(w)
		ctx := context.Background()
		if err := c.Reset(ctx); err != nil {
			t.Fatal(err)
		}
		for _, bound := range []int{16, 17, 64} {
			length, err := c.MeasureIR(ctx, bound)
			if err != nil || length != 16 {
				t.Fatalf("length %d: %v", length, err)
			}
			if w.taps[0].instruction != 15 || w.taps[1].instruction != 4095 || c.State() != Idle {
				t.Fatal("measurement did not leave all TAPs bypassed")
			}
		}
		if _, err := c.MeasureIR(ctx, 15); err == nil {
			t.Fatal("short bound accepted")
		}
		before := w.calls
		if _, err := c.MeasureIR(ctx, 0); err == nil || w.calls != before {
			t.Fatal("invalid bound clocked")
		}
	}
}
