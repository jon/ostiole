package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

type parityTarget struct {
	connectionTarget
	corrupt bool
	cause   error
	reads   int
}

func (t *parityTarget) Read(ctx context.Context, req sim.Request) (uint32, error) {
	value, err := t.connectionTarget.Read(ctx, req)
	if req.Read && req.Addr == 12 {
		t.reads++
		if t.corrupt {
			t.corrupt = false
			if t.cause != nil {
				return 0x12345678, t.cause
			}
			return 0x12345678, swd.ErrParity
		}
	}
	return value, err
}

type parityWire struct {
	*sim.Wire
	failures int
}

func (w *parityWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	input, err := w.Wire.SWDIO(ctx, direction, output, bits)
	if err != nil {
		w.failures++
	}
	return input, err
}

func TestWireEmitsReadParityWithoutLosingFraming(t *testing.T) {
	for _, overrun := range []bool{false, true} {
		t.Run(map[bool]string{false: "simple", true: "fixed"}[overrun], func(t *testing.T) {
			target := &parityTarget{}
			target.dpidr = 0x2ba01477
			target.ignoreORUN = !overrun
			wire := &parityWire{Wire: sim.New(target)}
			conn := swd.New(wire)
			if _, err := conn.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			target.corrupt = true
			before := target.reads
			if _, err := conn.ReadDP(t.Context(), 12); !errors.Is(err, swd.ErrParity) {
				t.Fatalf("read parity=%v", err)
			}
			if wire.failures != 0 {
				t.Fatalf("parity was a failed physical exchange: %d", wire.failures)
			}
			if target.reads != before+1 || target.corrupt {
				t.Fatal("parity injection did not execute exactly one read")
			}
			if _, err := conn.ReadDP(t.Context(), 12); err != nil {
				t.Fatalf("read after parity=%v", err)
			}
			if err := conn.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWireKeepsOtherTargetErrorsAsExchangeFailures(t *testing.T) {
	native := errors.New("target read failed")
	for _, cause := range []error{native, errors.Join(swd.ErrParity, native)} {
		target := &parityTarget{cause: cause}
		target.dpidr = 0x2ba01477
		wire := &parityWire{Wire: sim.New(target)}
		conn := swd.New(wire)
		if _, err := conn.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		target.corrupt = true
		_, err := conn.ReadDP(t.Context(), 12)
		if !errors.Is(err, native) || wire.failures != 1 {
			t.Fatalf("native error=%v exchange failures=%d", err, wire.failures)
		}
		if err := conn.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
