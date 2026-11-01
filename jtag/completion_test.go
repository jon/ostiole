package jtag_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/jtag"
)

// This table is independent of the connection's transition implementation.
var completionTransitions = map[string][2]string{
	"reset":     {"idle", "reset"},
	"idle":      {"idle", "selectDR"},
	"selectDR":  {"captureDR", "selectIR"},
	"captureDR": {"shiftDR", "exit1DR"},
	"shiftDR":   {"shiftDR", "exit1DR"},
	"exit1DR":   {"pauseDR", "updateDR"},
	"pauseDR":   {"pauseDR", "exit2DR"},
	"exit2DR":   {"shiftDR", "updateDR"},
	"updateDR":  {"idle", "selectDR"},
	"selectIR":  {"captureIR", "reset"},
	"captureIR": {"shiftIR", "exit1IR"},
	"shiftIR":   {"shiftIR", "exit1IR"},
	"exit1IR":   {"pauseIR", "updateIR"},
	"pauseIR":   {"pauseIR", "exit2IR"},
	"exit2IR":   {"shiftIR", "updateIR"},
	"updateIR":  {"idle", "selectDR"},
}

type completionTAP struct {
	expire                  bool
	stall                   bool
	state                   string
	shift, committed        uint16
	shifted, updates, calls int
	cancelAfter             int
	cancel                  context.CancelFunc
	contexts                []context.Context
}

func (*completionTAP) MaxTransferBits() int { return 4 }

func (w *completionTAP) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w.stall && w.shifted >= 4 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	w.calls++
	w.contexts = append(w.contexts, ctx)
	out := make([]byte, (bits+7)/8)
	for i := range bits {
		mode := int(tms[i/8] >> uint(i%8) & 1)
		switch w.state {
		case "captureDR", "captureIR":
			w.shift, w.shifted = 0xaaa, 0
		case "shiftDR", "shiftIR":
			out[i/8] |= byte(w.shift&1) << uint(i%8)
			w.shift = w.shift>>1 | uint16(tdi[i/8]>>uint(i%8)&1)<<11
			w.shifted++
			if w.cancel != nil && w.shifted == w.cancelAfter {
				w.cancel()
				w.cancel = nil
			}
		}
		w.state = completionTransitions[w.state][mode]
		if w.state == "updateDR" || w.state == "updateIR" {
			w.committed = w.shift
			w.updates++
		}
	}
	if w.expire && w.shifted >= 4 {
		<-ctx.Done()
	}
	return out, nil
}

func enteredCompletionTAP(t *testing.T, options ...jtag.Option) (*jtag.Conn, *completionTAP) {
	t.Helper()
	wire := &completionTAP{state: "reset"}
	conn := jtag.New(wire, options...)
	if err := conn.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Move(t.Context(), jtag.Idle); err != nil {
		t.Fatal(err)
	}
	return conn, wire
}

func TestCanceledScanCommitsOnlyCompleteRegister(t *testing.T) {
	for _, ir := range []bool{false, true} {
		for _, at := range []int{1, 4, 11, 12} {
			conn, wire := enteredCompletionTAP(t)
			ctx, cancel := context.WithCancel(t.Context())
			wire.cancel, wire.cancelAfter = cancel, at
			scan := conn.ScanDR
			if ir {
				scan = conn.ScanIR
			}
			captured, err := scan(ctx, []byte{0x37, 0x09}, 12)
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("IR%v clock%d: %v", ir, at, err)
			}
			if wire.committed != 0x937 || wire.updates != 1 || wire.shifted != 12 || wire.state != "idle" || conn.State() != jtag.Idle {
				t.Fatalf("IR%v clock%d: value%#x updates%d shifted%d oracle%s connection%v", ir, at, wire.committed, wire.updates, wire.shifted, wire.state, conn.State())
			}
			if len(captured) != 2 || captured[0] != 0xaa || captured[1]&0xf != 0xa {
				t.Fatalf("completed capture: %x", captured)
			}
			before := wire.updates
			if err := conn.Move(t.Context(), jtag.Idle); err != nil || wire.updates != before {
				t.Fatalf("later movement committed abandoned data: %v", err)
			}
		}
	}
}

func TestScanCompletionDeadlineBlocksPartialUpdate(t *testing.T) {
	t.Run("between-chunks", func(t *testing.T) { checkScanCompletionDeadline(t, false) })
	t.Run("wire-failure", func(t *testing.T) { checkScanCompletionDeadline(t, true) })
}

func checkScanCompletionDeadline(t *testing.T, stall bool) {
	t.Helper()
	conn, wire := enteredCompletionTAP(t, jtag.WithCompletionTimeout(100*time.Millisecond))
	wire.expire, wire.stall = !stall, stall
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire.cancel, wire.cancelAfter = cancel, 4
	_, err := conn.ScanDR(ctx, []byte{0x37, 0x09}, 12)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("incomplete scan lost errors: %v", err)
	}
	if conn.State() != jtag.Unknown || wire.updates != 0 {
		t.Fatalf("uncertain scan state%v committed%d", conn.State(), wire.updates)
	}
	before := wire.calls
	if err := conn.Move(t.Context(), jtag.Idle); !errors.Is(err, jtag.ErrStateUnknown) || wire.calls != before || wire.updates != 0 {
		t.Fatalf("partial scan committed through later movement: %v", err)
	}
}

func TestScanCompletionBudgetAndContextValues(t *testing.T) {
	type contextKey struct{}
	for _, budget := range []time.Duration{30 * time.Second, 90 * time.Second} {
		options := []jtag.Option{{}}
		if budget != 30*time.Second {
			options = append(options, jtag.WithCompletionTimeout(budget))
		}
		conn, wire := enteredCompletionTAP(t, options...)
		before := len(wire.contexts)
		ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), contextKey{}, "scan"), 100*time.Millisecond)
		_, err := conn.ScanDR(ctx, []byte{0x37, 0x09}, 12)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range wire.contexts[before:] {
			deadline, ok := got.Deadline()
			if got == ctx || !ok || time.Until(deadline) < budget/2 || time.Until(deadline) > budget || got.Value(contextKey{}) != "scan" {
				t.Fatal("lost independent completion budget or context value")
			}
		}
	}
}

func TestScanPreflightAndInvalidBudgetSendNoTraffic(t *testing.T) {
	for _, budget := range []time.Duration{0, -time.Second} {
		wire := &completionTAP{state: "reset"}
		conn := jtag.New(wire, jtag.WithCompletionTimeout(budget))
		if err := conn.Reset(t.Context()); err == nil || wire.calls != 0 {
			t.Fatalf("invalid budget reached wire: %v", err)
		}
	}
	conn, wire := enteredCompletionTAP(t)
	before := wire.calls
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := conn.ScanDR(ctx, []byte{1}, 1); !errors.Is(err, context.Canceled) || wire.calls != before {
		t.Fatalf("preflight cancellation: %v", err)
	}
	for _, bits := range []int{-1, 0, 9, jtag.MaxScanBits + 1} {
		if _, err := conn.ScanDR(t.Context(), []byte{1}, bits); err == nil || wire.calls != before {
			t.Fatalf("invalid scan reached wire: %v", err)
		}
	}
}
