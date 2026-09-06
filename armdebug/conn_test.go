package armdebug_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/probe"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type observedTarget struct {
	*dapsim.Target
	resets int
}

func (t *observedTarget) ObserveLineReset() {
	t.resets++
	t.Target.ObserveLineReset()
}

type bench struct {
	*swdsim.Wire
	target                           *observedTarget
	activations, closes, transfers   int
	activationErr, closeErr, wireErr error
	beforeClose                      func()
}

func newBench() *bench {
	target := &observedTarget{Target: dapsim.New(0x2ba01477)}
	return &bench{Wire: swdsim.New(target), target: target}
}

func (b *bench) SWD(context.Context, probe.SWDConfig) (probe.Wire, error) {
	b.activations++
	return b, b.activationErr
}

func (b *bench) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	b.transfers++
	if b.wireErr != nil {
		return nil, b.wireErr
	}
	return b.Wire.SWDIO(ctx, direction, output, bits)
}

func (b *bench) Close() error {
	b.closes++
	if b.beforeClose != nil {
		b.beforeClose()
	}
	return b.closeErr
}

func config() armdebug.Config {
	return armdebug.Config{Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 100_000})}
}

func TestConnectOwnsDAPAndProbeCleanup(t *testing.T) {
	b := newBench()
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{Serial: "one"}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	if c.Info().Serial != "one" || c.Port() == nil || b.activations != 1 {
		t.Fatal("connection did not lend its port")
	}
	if b.target.resets != 2 {
		t.Fatalf("expected one JTAG-to-SWD entry, observed %d resets", b.target.resets)
	}
	if _, err := c.Port().ReadDP(t.Context(), dap.DPIDR); err != nil {
		t.Fatal(err)
	}
	b.beforeClose = func() {
		if b.target.OverrunDetectEnabled() {
			t.Fatal("probe closed before SWD restoration")
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.Port() != nil || b.closes != 1 || c.Info().Serial != "one" {
		t.Fatal("close state")
	}
	if err := c.Close(); err != nil || b.closes != 1 {
		t.Fatal("repeated close")
	}
}

func TestConnectPreflightCleansTransferredProbe(t *testing.T) {
	for _, cfg := range []armdebug.Config{{}, {Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 999})}} {
		b := newBench()
		c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), cfg)
		if err == nil || c != nil || b.activations != 0 || b.transfers != 0 || b.closes != 1 {
			t.Fatalf("preflight: owner=%v err=%v bench=%+v", c, err, b)
		}
	}
	for _, ctx := range []context.Context{nil, canceledContext(t)} {
		b := newBench()
		c, err := armdebug.Connect(ctx, probe.New(probe.Info{}, b), config())
		if err == nil || c != nil || b.activations != 0 || b.closes != 1 {
			t.Fatal("context preflight")
		}
	}
}

func canceledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func TestFailedReleaseRetainsProbeForRetry(t *testing.T) {
	b := newBench()
	ctx, cancel := context.WithCancel(t.Context())
	c, err := armdebug.Connect(ctx, probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	want := errors.New("wire failed")
	b.wireErr = want
	if err := c.Close(); !errors.Is(err, want) || b.closes != 0 || c.Port() != nil {
		t.Fatalf("release did not retain probe: %v", err)
	}
	b.wireErr = nil
	if err := c.Close(); err != nil || b.closes != 1 {
		t.Fatalf("retry with canceled operation: %v", err)
	}
}

func TestFailedSetupRetainsCleanupOwner(t *testing.T) {
	b := newBench()
	want, cleanup := errors.New("activation"), errors.New("close")
	b.activationErr, b.closeErr = want, cleanup
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if c == nil || c.Port() != nil || !errors.Is(err, want) || !errors.Is(err, cleanup) {
		t.Fatalf("lost cleanup: %v", err)
	}
	b.closeErr = nil
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestZeroAndNilOwners(t *testing.T) {
	for _, c := range []*armdebug.Conn{nil, {}} {
		if c.Close() != nil || c.Port() != nil || c.Info() != (probe.Info{}) {
			t.Fatal("zero owner")
		}
	}
	if c, err := armdebug.Connect(t.Context(), nil, config()); c != nil || err == nil {
		t.Fatal("nil probe connected")
	}
}
