package sim_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

// dormantWire models an interface which ignores ordinary SWD until the
// selection alert and SWD activation code have been received.
type dormantWire struct {
	inner            *sim.Wire
	active           bool
	alerted          bool
	entries          int
	activations      int
	requests         int
	failBits         int
	failErr          error
	absent           bool
	cancelAfterAlert context.CancelFunc
}

func (w *dormantWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if err := w.validateCall(ctx, output, bits); err != nil {
		return nil, err
	}
	switch {
	case bits == 40 && bytes.Equal(output, []byte{0xff, 0x75, 0x77, 0x77, 0x67}):
		w.entries++
		w.active, w.alerted = false, false
	case bits == 136 && bytes.Equal(output, []byte{0xff, 0x92, 0xf3, 0x09, 0x62, 0x95, 0x2d, 0x85, 0x86, 0xe9, 0xaf, 0xdd, 0xe3, 0xa2, 0x0e, 0xbc, 0x19}):
		w.alerted = true
		if w.cancelAfterAlert != nil {
			w.cancelAfterAlert()
			w.cancelAfterAlert = nil
		}
	case bits == 76 && w.alerted && bytes.Equal(output, []byte{0xa0, 0xf1, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x0f, 0x00}):
		w.activations++
		w.active, w.alerted = !w.absent, false
		if _, err := w.inner.SWDIO(ctx, bytes.Repeat([]byte{0xff}, 8), []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0}, 64); err != nil {
			return nil, err
		}
	default:
		if w.active {
			return w.inner.SWDIO(ctx, direction, output, bits)
		}
		if bits == 12 {
			w.requests++
		}
		return bytes.Repeat([]byte{0xff}, (bits+7)/8), nil
	}
	for bit := range bits {
		if direction[bit/8]&(1<<uint(bit%8)) == 0 {
			return nil, errors.New("activation must drive every clock")
		}
	}
	return make([]byte, (bits+7)/8), nil
}

func TestDormantConnectionAndReleaseRepair(t *testing.T) {
	for _, inherited := range []uint32{0, testOverrunDetect} {
		target := &connectionTarget{dpidr: 0x4c013477, ctrlStat: inherited}
		wire := &dormantWire{inner: sim.New(target)}
		conn := swd.New(wire)
		if got, err := conn.Connect(t.Context()); err != nil || got != target.dpidr {
			t.Fatalf("Connect() = %#x, %v", got, err)
		}
		if wire.entries != 1 || wire.activations != 1 || wire.requests != 1 {
			t.Fatalf("entry counts = %+v", wire)
		}
		wire.active = false
		if err := conn.LineReset(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := conn.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if wire.activations != 2 || target.ctrlStat&testOverrunDetect != inherited {
			t.Fatalf("release activations=%d CTRL=%#x", wire.activations, target.ctrlStat)
		}
	}
}

func TestDormantActivationFailureRetainsCleanup(t *testing.T) {
	for _, bits := range []int{40, 136, 76} {
		target := &connectionTarget{dpidr: 0x4c013477}
		failure := errors.New("activation transport failed")
		wire := &dormantWire{inner: sim.New(target), failBits: bits, failErr: failure}
		conn := swd.New(wire)
		if _, err := conn.Connect(t.Context()); !errors.Is(err, failure) {
			t.Fatalf("%d-bit failure: %v", bits, err)
		}
		if _, err := conn.ReadDP(t.Context(), 0); err == nil {
			t.Fatal("read allowed with cleanup pending")
		}
		wire.failBits = 0
		if err := conn.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if target.ctrlStat&testOverrunDetect != 0 {
			t.Fatal("release acquired overrun state")
		}
	}
}

func TestDormantAbsentTargetHasBoundedAttempts(t *testing.T) {
	wire := &dormantWire{inner: sim.New(&connectionTarget{dpidr: 0x4c013477}), absent: true}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); !errors.Is(err, swd.ErrProtocol) {
		t.Fatalf("Connect(): %v", err)
	}
	if wire.entries != 2 || wire.activations != 2 || wire.requests != 4 {
		t.Fatalf("connection plus cleanup counts = %+v", wire)
	}
}

func TestDormantActivationCancellationUsesIndependentCleanup(t *testing.T) {
	target := &connectionTarget{dpidr: 0x4c013477}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wire := &dormantWire{inner: sim.New(target), cancelAfterAlert: cancel}
	conn := swd.New(wire)
	if _, err := conn.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect(): %v", err)
	}
	if wire.entries != 2 || wire.activations != 1 {
		t.Fatalf("entry/activation counts = %d/%d", wire.entries, wire.activations)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestDormantRecoveryRejectsChangedIdentityBeforeRestoration(t *testing.T) {
	target := &connectionTarget{dpidr: 0x4c013477}
	wire := &dormantWire{inner: sim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	writes := target.ctrlWrites
	wire.active = false
	target.dpidr = 0x2ba01477
	if err := conn.LineReset(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Release(t.Context()); err == nil {
		t.Fatal("changed identity accepted")
	}
	if target.ctrlWrites != writes {
		t.Fatal("restored state to a different target")
	}
	target.dpidr = 0x4c013477
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.ctrlStat&testOverrunDetect != 0 {
		t.Fatal("owned overrun state not restored")
	}
}

func TestDormantFallbackRequiresCompletedInvalidACK(t *testing.T) {
	failure := errors.New("invalid ACK tail failed")
	wire := &dormantWire{inner: sim.New(&connectionTarget{dpidr: 0x4c013477}), failBits: 42, failErr: failure}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); !errors.Is(err, swd.ErrProtocol) || !errors.Is(err, failure) {
		t.Fatalf("Connect(): %v", err)
	}
	if wire.entries != 0 {
		t.Fatal("activation sent after failed turnaround")
	}
}

type entryReadFailure struct {
	*connectionTarget
	err error
}

func (t *entryReadFailure) Read(ctx context.Context, req sim.Request) (uint32, error) {
	if req.Read && !req.AP && req.Addr == 0 {
		return 0, t.err
	}
	return t.connectionTarget.Read(ctx, req)
}

func TestDormantFallbackDoesNotReplayOtherIdentityFailures(t *testing.T) {
	for _, failure := range []error{swd.ErrWait, swd.ErrFault, swd.ErrParity, errors.New("USB failure")} {
		target := &entryReadFailure{connectionTarget: &connectionTarget{dpidr: 0x4c013477}, err: failure}
		wire := &dormantWire{inner: sim.New(target), active: true}
		conn := swd.New(wire)
		if _, err := conn.Connect(t.Context()); !errors.Is(err, failure) {
			t.Fatalf("Connect(): %v, want %v", err, failure)
		}
		if wire.entries != 0 {
			t.Fatalf("activation sent after %v", failure)
		}
	}
}

func TestDormantFallbackLeavesLegacyEntryUnchanged(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477}
	wire := &dormantWire{inner: sim.New(target), active: true}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if wire.entries != 0 {
		t.Fatal("legacy target received dormant activation")
	}
}

func (w *dormantWire) validateCall(ctx context.Context, output []byte, bits int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if bits > 136 {
		return errors.New("entry exceeds legacy wire limit")
	}
	if bits == w.failBits && (bits != 136 || output[1] == 0x92) {
		return w.failErr
	}

	return nil
}
