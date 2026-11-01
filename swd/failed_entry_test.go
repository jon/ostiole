package swd_test

import (
	"context"
	"errors"
	"testing"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type entryFailureWire struct {
	inner swd.Wire
	err   error
	calls int
}

func (w *entryFailureWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	if w.err != nil {
		return nil, w.err
	}
	if w.inner == nil {
		return make([]byte, (bits+7)/8), nil
	}
	return w.inner.SWDIO(ctx, direction, output, bits)
}

func TestFirstFailedSWDEntryOwnsNoRestoration(t *testing.T) {
	failure := errors.New("poisoned entry")
	for _, cause := range []error{failure, nil} {
		wire := &entryFailureWire{err: cause}
		conn := swd.New(wire)
		_, err := conn.Connect(t.Context())
		if err == nil || cause != nil && !errors.Is(err, cause) {
			t.Fatalf("entry result: %v", err)
		}
		before := wire.calls
		if err := conn.Release(t.Context()); err != nil || wire.calls != before {
			t.Fatalf("unowned restoration sent traffic: %v calls %d -> %d", err, before, wire.calls)
		}
		if cause != nil && before != 1 {
			t.Fatalf("failed first entry retried during cleanup: %d calls", before)
		}
	}
}

func TestFailedSWDReentryRetainsOriginalRestoration(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	wire := &entryFailureWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("missing acquired state")
	}
	failure := errors.New("lost wire")
	wire.err = failure
	if _, err := conn.Connect(t.Context()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := conn.Release(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("discarded acquired state: %v", err)
	}
	wire.err = nil
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.OverrunDetectEnabled() {
		t.Fatal("original ORUNDETECT was not restored")
	}
}

func TestSWDPreflightErrorsAreNotExecuted(t *testing.T) {
	wire := &entryFailureWire{inner: swdsim.New(dapsim.New(0x2ba01477))}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var missing *swd.Conn
	calls := []func() error{
		func() error { _, err := missing.ReadDP(t.Context(), 0); return err },
		func() error { _, err := swd.New(wire).ReadDP(t.Context(), 0); return err },
		func() error { _, err := conn.ReadDP(ctx, 0); return err },
		func() error { return conn.WriteDP(t.Context(), 4, 0) },
		func() error { _, err := conn.ReadDP(t.Context(), 1); return err },
		func() error { return conn.WriteAP(t.Context(), 1, 0) },
	}
	before := wire.calls
	for _, call := range calls {
		if err := call(); !errors.Is(err, swd.ErrNotExecuted) {
			t.Fatalf("preflight result: %v", err)
		}
	}
	if wire.calls != before {
		t.Fatal("preflight sent traffic")
	}
	if _, err := conn.ReadDP(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
