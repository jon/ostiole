package armdebug_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
)

func TestJTAGPreflightBeforeDiscoveryOrActivation(t *testing.T) {
	arm, _ := jtag.IDCODE(4, 0x5ba00477)
	wide, _ := jtag.IDCODE(12, 0x14730093)
	bypass, _ := jtag.Bypass(4)
	clock := probe.JTAGConfig{MaxClockHz: 100_000}
	ports := []armdebug.PortConfig{
		armdebug.JTAGDP(clock, nil, 0),
		armdebug.JTAGDP(clock, jtag.Layout{{}}, 0),
		armdebug.JTAGDP(clock, make(jtag.Layout, 1025), 0),
		armdebug.JTAGDP(clock, jtag.Layout{arm}, -1),
		armdebug.JTAGDP(clock, jtag.Layout{arm}, 1),
		armdebug.JTAGDP(clock, jtag.Layout{wide}, 0),
		armdebug.JTAGDP(clock, jtag.Layout{bypass}, 0),
		armdebug.JTAGDP(probe.JTAGConfig{MaxClockHz: 999}, jtag.Layout{arm}, 0),
	}
	f := registerOpenFixture(t)
	for _, port := range ports {
		cfg := armdebug.Config{Port: port}
		if c, err := armdebug.Open(t.Context(), discover.Selection{Provider: f.id}, cfg); c != nil || err == nil || f.enumerations != 0 {
			t.Fatal("invalid JTAG configuration reached discovery")
		}
		b := newBench()
		c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), cfg)
		if c != nil || err == nil || b.activations != 0 || b.transfers != 0 || b.closes != 1 {
			t.Fatalf("invalid JTAG configuration acquired protocol state: %v", err)
		}
	}
}

func TestJTAGConfigCopiesLayoutAndRejectsUnsupportedProbe(t *testing.T) {
	arm, _ := jtag.IDCODE(4, 0x5ba00477)
	layout := jtag.Layout{arm}
	port := armdebug.JTAGDP(probe.JTAGConfig{MaxClockHz: 100_000}, layout, 0)
	layout[0] = jtag.TAPSpec{}
	b := &closeOnly{}
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), armdebug.Config{Port: port})
	if c != nil || !errors.Is(err, probe.ErrUnsupportedJTAG) || !b.closed {
		t.Fatalf("copied JTAG configuration: %v", err)
	}
}

func TestCleanupBudgetValidationAndSWDRelease(t *testing.T) {
	cfg := config()
	cfg.CleanupTimeout = -time.Second
	f := registerOpenFixture(t)
	if c, err := armdebug.Open(t.Context(), discover.Selection{Provider: f.id}, cfg); c != nil || err == nil || f.enumerations != 0 {
		t.Fatal("invalid cleanup budget reached discovery")
	}
	for _, budget := range []time.Duration{0, 4 * time.Second} {
		b := newBench()
		cfg.CleanupTimeout = budget
		c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), cfg)
		if err != nil {
			t.Fatal(err)
		}
		// Each started SWD frame has its own one-second completion budget.
		b.beforeTransfer = func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			want := time.Second
			if !ok || time.Until(deadline) < want/2 || time.Until(deadline) > want {
				t.Fatal("release did not use the configured independent budget")
			}
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConnectRejectsNegativeCleanupBudgetBeforeActivation(t *testing.T) {
	cfg := config()
	cfg.CleanupTimeout = -time.Second
	b := newBench()
	if c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), cfg); c != nil || err == nil || b.activations != 0 || b.transfers != 0 || b.closes != 1 {
		t.Fatalf("invalid cleanup budget activated probe: %v, %+v", err, b)
	}
}

type jtagPreflightBackend struct{ activations, clocks, closes int }

func (b *jtagPreflightBackend) Close() error { b.closes++; return nil }
func (b *jtagPreflightBackend) JTAG(context.Context, probe.JTAGConfig) (probe.JTAGWire, error) {
	b.activations++
	return b, nil
}
func (b *jtagPreflightBackend) JTAGIO(context.Context, []byte, []byte, int) ([]byte, error) {
	b.clocks++
	return nil, errors.New("unexpected JTAG clocks")
}
func (*jtagPreflightBackend) MaxTransferBits() int { return 64 }

func TestJTAGLayoutValidationUsesUsableSelectedTAP(t *testing.T) {
	arm, _ := jtag.IDCODE(4, 0x5ba00477)
	oversized := make(jtag.Layout, 1025)
	for i := range oversized {
		oversized[i] = arm
	}
	clock := probe.JTAGConfig{MaxClockHz: 100_000}
	control := &jtagPreflightBackend{}
	p := probe.New(probe.Info{}, control)
	if _, err := p.JTAG(t.Context(), clock); err != nil || control.activations != 1 {
		t.Fatalf("control backend lacks JTAG: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	f := registerOpenFixture(t)
	for _, layout := range []jtag.Layout{{arm, {}}, oversized} {
		cfg := armdebug.Config{Port: armdebug.JTAGDP(clock, layout, 0)}
		if c, err := armdebug.Open(t.Context(), discover.Selection{Provider: f.id}, cfg); c != nil || err == nil || f.enumerations != 0 {
			t.Fatalf("invalid layout enumerated: %v", err)
		}
		b := &jtagPreflightBackend{}
		if c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), cfg); c != nil || err == nil || b.activations != 0 || b.clocks != 0 || b.closes != 1 {
			t.Fatalf("invalid layout activated: %v, %+v", err, b)
		}
	}
}
