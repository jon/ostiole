package jtag

import (
	"context"
	"errors"
	"testing"
)

type wireFake struct {
	limit, calls, fail int
	tms, tdi           []bool
}

func (w *wireFake) MaxTransferBits() int { return w.limit }
func (w *wireFake) JTAGIO(_ context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	w.calls++
	if bits > w.limit {
		panic("transfer exceeds limit")
	}
	for i := range bits {
		w.tms = append(w.tms, tms[i/8]&(1<<uint(i%8)) != 0)
		w.tdi = append(w.tdi, tdi[i/8]&(1<<uint(i%8)) != 0)
	}
	if w.calls == w.fail {
		return nil, errors.New("broken wire")
	}
	return append([]byte(nil), tdi...), nil
}

func TestResetAndMove(t *testing.T) {
	w := &wireFake{limit: 3}
	c := New(w)
	ctx := context.Background()
	if err := c.Move(ctx, Idle); !errors.Is(err, ErrStateUnknown) {
		t.Fatalf("Move before reset: %v", err)
	}
	if w.calls != 0 {
		t.Fatal("constructor or invalid move sent traffic")
	}
	if err := c.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.tms) != 5 {
		t.Fatalf("reset clocks: %d", len(w.tms))
	}
	for _, bit := range w.tms {
		if !bit {
			t.Fatal("reset drove TMS low")
		}
	}
	for target := Reset; target <= UpdateIR; target++ {
		if err := c.Move(ctx, target); err != nil {
			t.Fatal(err)
		}
		if c.State() != target {
			t.Fatalf("state %v, want %v", c.State(), target)
		}
	}
}

func TestFailureRequiresReset(t *testing.T) {
	w := &wireFake{limit: 2, fail: 2}
	c := New(w)
	ctx := context.Background()
	if err := c.Reset(ctx); err == nil {
		t.Fatal("missing wire error")
	}
	if c.State() != Unknown {
		t.Fatal("failed reset retained state")
	}
	before := w.calls
	if err := c.Move(ctx, Idle); !errors.Is(err, ErrStateUnknown) {
		t.Fatal(err)
	}
	if before != w.calls {
		t.Fatal("implicitly recovered")
	}
	w.fail = 0
	if err := c.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.Move(ctx, Idle); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if c.State() != Reset {
		t.Fatal("unsent cancellation lost state")
	}
}

func TestInvalidInputsBeforeTraffic(t *testing.T) {
	w := &wireFake{limit: 8}
	c := New(w)
	var absent context.Context
	if err := c.Reset(absent); err == nil {
		t.Fatal("nil context accepted")
	}
	if err := c.Move(context.Background(), Unknown); err == nil {
		t.Fatal("unknown destination accepted")
	}
	if w.calls != 0 {
		t.Fatal("invalid input clocked")
	}
	if err := New(nil).Reset(context.Background()); err == nil {
		t.Fatal("nil wire accepted")
	}
	w.limit = 0
	if err := c.Reset(context.Background()); err == nil {
		t.Fatal("zero limit accepted")
	}
}

func TestKnownPaths(t *testing.T) {
	for _, tt := range []struct {
		from, to State
		want     string
	}{
		{Reset, Idle, "0"}, {Idle, ShiftDR, "100"},
		{Idle, ShiftIR, "1100"}, {ShiftDR, Idle, "110"},
		{ShiftIR, Idle, "110"}, {PauseDR, ShiftDR, "10"},
		{PauseIR, ShiftIR, "10"}, {UpdateIR, ShiftDR, "100"},
	} {
		bits := path(tt.from, tt.to)
		got := ""
		for _, bit := range bits {
			if bit {
				got += "1"
			} else {
				got += "0"
			}
		}
		if got != tt.want {
			t.Errorf("%v -> %v = %s, want %s", tt.from, tt.to, got, tt.want)
		}
	}
	for from := Reset; from <= UpdateIR; from++ {
		for to := Reset; to <= UpdateIR; to++ {
			state := from
			bits := path(from, to)
			if len(bits) > 8 {
				t.Fatalf("unbounded path %v -> %v", from, to)
			}
			for _, bit := range bits {
				index := 0
				if bit {
					index = 1
				}
				state = transitions[state][index]
			}
			if state != to {
				t.Fatalf("unreachable %v -> %v", from, to)
			}
		}
	}
}
