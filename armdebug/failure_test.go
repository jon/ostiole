package armdebug_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/probe"
)

type closeOnly struct{ closed bool }

func (b *closeOnly) Close() error { b.closed = true; return nil }

func TestUnsupportedSWDCleansOwner(t *testing.T) {
	b := &closeOnly{}
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if c != nil || !errors.Is(err, probe.ErrUnsupportedSWD) || !b.closed {
		t.Fatalf("unsupported: %v", err)
	}
}

func TestProtocolSetupFailureRetainsLiveDependencies(t *testing.T) {
	b := newBench()
	want := errors.New("wire unavailable")
	b.wireErr = want
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if c == nil || c.Port() != nil || !errors.Is(err, want) || b.closes != 0 {
		t.Fatalf("setup cleanup: %v", err)
	}
	b.wireErr = nil
	if err := c.Close(); err != nil || b.closes != 1 {
		t.Fatalf("retry setup cleanup: %v", err)
	}
}

func TestProbeCloseRetryDoesNotRepeatDAPRelease(t *testing.T) {
	b := newBench()
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("host close")
	b.closeErr = want
	if err := c.Close(); !errors.Is(err, want) {
		t.Fatal(err)
	}
	transfers := b.transfers
	b.closeErr = nil
	if err := c.Close(); err != nil || b.transfers != transfers || b.closes != 2 {
		t.Fatalf("host retry repeated target traffic: %v", err)
	}
}
