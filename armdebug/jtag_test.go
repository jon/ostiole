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
		// The wire records the independent context supplied to ordinary release.
		b.beforeTransfer = func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			want := budget
			if want == 0 {
				want = time.Second
			}
			if !ok || time.Until(deadline) < want/2 || time.Until(deadline) > want {
				t.Fatal("release did not use the configured independent budget")
			}
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
