package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

func TestSimulatorRejectsHighIdleCycles(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		for _, read := range []bool{false, true} {
			rejectHighIdle(t, fixed, read)
		}
	}
}

func rejectHighIdle(t *testing.T, fixed, read bool) {
	t.Helper()
	target := &connectionTarget{dpidr: 0x2ba01477, ignoreORUN: !fixed}
	if fixed {
		target.ctrlStat = 1
	}
	wire := sim.New(target)
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	header := byte(0x81)
	if read {
		header = 0xa5
	}
	if !fixed {
		if _, err := wire.SWDIO(t.Context(), []byte{0xff, 0}, []byte{header, 0}, 12); err != nil {
			t.Fatal(err)
		}
	}
	bits, idle, start := 42, 34, 0
	if fixed {
		bits, idle, start = 54, 46, 12
	}
	direction, output := make([]byte, (bits+7)/8), make([]byte, (bits+7)/8)
	if fixed {
		direction[0], output[0] = 0xff, header
	}
	if !read {
		for i := start + 1; i < bits; i++ {
			direction[i/8] |= 1 << uint(i%8)
		}
	} else {
		for i := idle; i < bits; i++ {
			direction[i/8] |= 1 << uint(i%8)
		}
	}
	output[idle/8] |= 1 << uint(idle%8)
	if _, err := wire.SWDIO(t.Context(), direction, output, bits); err == nil {
		t.Fatalf("accepted high idle: fixed%v read%v", fixed, read)
	}
}

func TestSimulatorDoesNotResetAwayPendingReadData(t *testing.T) {
	wire := sim.New(&connectionTarget{dpidr: 0x2ba01477, ignoreORUN: true})
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.SWDIO(t.Context(), []byte{0xff, 0}, []byte{0xa5, 0}, 12); err != nil {
		t.Fatal(err)
	}
	if err := conn.JTAGToSWD(t.Context()); err == nil {
		t.Fatal("entry drove SWDIO against a pending read")
	}
	direction := []byte{0, 0, 0, 0, 0xfc, 3}
	if _, err := wire.SWDIO(t.Context(), direction, make([]byte, 6), 42); err != nil {
		t.Fatalf("entry discarded pending read: %v", err)
	}
}

type cancelAfterACKWire struct {
	swd.Wire
	cancel context.CancelFunc
}

func (w *cancelAfterACKWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := w.Wire.SWDIO(ctx, direction, output, bits)
	if err == nil && bits == 12 && w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	return input, err
}

func TestCanceledWriteCannotBeCompletedByLaterEntryBits(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, ignoreORUN: true}
	wire := &cancelAfterACKWire{Wire: sim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire.cancel = cancel
	if err := conn.WriteDP(ctx, 0, 0x13579bdf); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("fresh entry encountered pending data: %v", err)
	}
	count := 0
	for _, value := range target.abortWrites {
		if value == 0x13579bdf {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("canceled write was changed or replayed: %#x", target.abortWrites)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
