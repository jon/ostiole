package swd

import (
	"context"
	"errors"
	"testing"
	"time"
)

type completedPhase struct {
	direction, output []byte
	bits              int
	ctx               context.Context
}

type completionWire struct {
	inner  Wire
	phases []completedPhase
	before func(context.Context, int)
	after  func(int)
}

func (w *completionWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if w.before != nil {
		w.before(ctx, len(w.phases)+1)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := w.inner.SWDIO(ctx, direction, output, bits)
	w.phases = append(w.phases, completedPhase{append([]byte(nil), direction...), append([]byte(nil), output...), bits, ctx})
	if w.after != nil {
		w.after(len(w.phases))
	}
	return result, err
}

func TestCanceledSimpleFrameFinishesEveryPhase(t *testing.T) {
	for _, ack := range []byte{1, 2, 4, 0} {
		for _, read := range []bool{false, true} {
			target := &targetWire{ack: ack, readValue: 0x12345678}
			wire := &completionWire{inner: target}
			conn := readyConn(wire)
			ctx, cancel := context.WithCancel(t.Context())
			wire.after = func(phase int) {
				if phase == 1 {
					cancel()
				}
			}
			_, err := conn.transfer(ctx, mustRequest(true, read, 0xc), 0x13579bdf)
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("ACK%d read%v: %v", ack, read, err)
			}
			if target.calls != 2 {
				t.Fatalf("ACK%d read%v left frame unfinished: %d phases", ack, read, target.calls)
			}
			if ack == 1 && !read && target.written != 0x13579bdf {
				t.Fatalf("canceled write changed value: %#x", target.written)
			}
			if len(wire.phases) != 2 || wire.phases[0].ctx != wire.phases[1].ctx || wire.phases[0].ctx == ctx {
				t.Fatal("frame did not share an independent context")
			}
			deadline, ok := wire.phases[0].ctx.Deadline()
			if !ok || time.Until(deadline) > time.Second {
				t.Fatal("frame completion is unbounded")
			}
		}
	}
}

func TestCancellationDuringFixedFrameDoesNotStopCommittedBits(t *testing.T) {
	target := &fixedWire{ack: 1}
	wire := &completionWire{inner: target}
	conn := readyConn(wire)
	conn.response = responseOverrun
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire.before = func(context.Context, int) { cancel() }
	err := conn.WriteAP(ctx, 0xc, 0x13579bdf)
	if !errors.Is(err, context.Canceled) || target.calls != 1 || target.written != 0x13579bdf {
		t.Fatalf("fixed frame interrupted: %v, calls%d value%#x", err, target.calls, target.written)
	}
}

func TestInvalidACKFixedWriteRetainsCommittedDataPhase(t *testing.T) {
	wire := &completionWire{inner: &fixedWire{ack: 0}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	if err := conn.WriteAP(t.Context(), 0xc, 0x13579bdf); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
	if len(wire.phases) != 1 || wire.phases[0].bits != 54 {
		t.Fatal("fixed-frame error replayed or appended back-off")
	}
	phase := wire.phases[0]
	for i := 13; i < 46; i++ {
		if !testBit(phase.direction, i) {
			t.Fatalf("fixed write did not retain driven clock%d", i)
		}
	}
	for i := range 32 {
		if testBit(phase.output, 13+i) != (uint32(0x13579bdf)>>uint(i)&1 != 0) {
			t.Fatalf("changed committed data bit%d", i)
		}
	}
	if _, err := conn.ReadDP(t.Context(), 0); err == nil || len(wire.phases) != 1 {
		t.Fatal("uncertain protocol reused without repair")
	}
}

func TestCompletionBudgetIsIndependentAndConfigurable(t *testing.T) {
	type contextKey struct{}
	for _, budget := range []time.Duration{time.Second, 4 * time.Second} {
		wire := &completionWire{inner: &targetWire{ack: 1}}
		conn := New(wire, WithCompletionTimeout(budget))
		conn.state = connectionReady
		conn.bank = bankSelection{valid: true}
		parent := context.WithValue(t.Context(), contextKey{}, "frame")
		ctx, cancel := context.WithTimeout(parent, 100*time.Millisecond)
		wire.before = func(got context.Context, _ int) {
			deadline, ok := got.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining < budget/2 || remaining > budget || got.Value(contextKey{}) != "frame" {
				t.Fatalf("completion context: deadline%v budget%v value%v", remaining, budget, got.Value(contextKey{}))
			}
		}
		_, err := conn.ReadAP(ctx, 0xc)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompletionDeadlineRetainsUncertaintyWithoutReplay(t *testing.T) {
	wire := &completionWire{inner: &targetWire{ack: 1}}
	conn := readyConn(wire)
	conn.completionTimeout = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire.before = func(got context.Context, phase int) {
		if phase == 2 {
			cancel()
			<-got.Done()
		}
	}
	err := conn.WriteAP(ctx, 0xc, 0x13579bdf)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotExecuted) {
		t.Fatalf("lost started-frame errors: %v", err)
	}
	if len(wire.phases) != 1 {
		t.Fatal("unexpected completed data phase")
	}
	if err := conn.WriteAP(t.Context(), 0xc, 0); !errors.Is(err, ErrNotExecuted) || len(wire.phases) != 1 {
		t.Fatalf("uncertain write replayed: %v", err)
	}
}

type limitedCompletionWire struct {
	*completionWire
	limit int
}

func (w *limitedCompletionWire) MaxTransferBits() int { return w.limit }

func TestCanceledBatchRetainsCompletedFrameAndUnsentSuffix(t *testing.T) {
	for _, packed := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		wire := &completionWire{}
		conn := readyConn(wire)
		if packed {
			wire.inner = &batchWire{values: []uint32{42}}
			wire.before = func(context.Context, int) { cancel() }
			conn.wire = &limitedCompletionWire{completionWire: wire, limit: 54}
			conn.response = responseOverrun
		} else {
			wire.inner = &repeatedTargetWire{target: targetWire{ack: 1, readValue: 42}}
			wire.after = func(phase int) {
				if phase == 1 {
					cancel()
				}
			}
		}
		batch := conn.NewBatch()
		first, next := batch.ReadAP(0xc), batch.ReadAP(0xc)
		err := batch.Commit(ctx)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("packed%v: %v", packed, err)
		}
		if value, err := first.Value(); err != nil || value != 42 {
			t.Fatalf("completed packed%v: %d,%v", packed, value, err)
		}
		if _, err := next.Value(); !errors.Is(err, ErrNotExecuted) {
			t.Fatalf("unsent packed%v: %v", packed, err)
		}
	}
}
