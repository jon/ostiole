package dap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
)

// These composition tests reuse the JTAG-DP peer instead of implementing
// another wire protocol in the armdebug tests.
type armJTAGBench struct {
	*jtagDPWire
	activations, closes     int
	activationErr, closeErr error
	beforeTransfer          func(context.Context) error
}

func (b *armJTAGBench) JTAG(_ context.Context, cfg probe.JTAGConfig) (probe.JTAGWire, error) {
	b.activations++
	if cfg.MaxClockHz != 100_000 {
		return nil, errors.New("unexpected JTAG clock")
	}
	return b, b.activationErr
}

func (b *armJTAGBench) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	if b.beforeTransfer != nil {
		if err := b.beforeTransfer(ctx); err != nil {
			return nil, err
		}
	}
	return b.jtagDPWire.JTAGIO(ctx, tms, tdi, bits)
}

func (b *armJTAGBench) Close() error { b.closes++; return b.closeErr }

func armJTAGFixture(t *testing.T, ir, index int) (*jtagDPModel, *armJTAGBench, armdebug.Config) {
	t.Helper()
	m, wire, chain := jtagModelChain(t, ir, index)
	target := sim.New(0x2ba01477)
	for ap := range 2 {
		if err := target.AddMEMAP(dap.NewAPSel(uint8(ap)), 0x24770011, map[uint32]uint32{0x1000: uint32(ap + 10)}); err != nil {
			t.Fatal(err)
		}
	}
	shareAPModel(t, m, target)
	b := &armJTAGBench{jtagDPWire: wire}
	cfg := armdebug.Config{Port: armdebug.JTAGDP(probe.JTAGConfig{MaxClockHz: 100_000}, chain.Layout(), index)}
	return m, b, cfg
}

func TestArmJTAGConnectionAndMemoryOwnership(t *testing.T) {
	for _, ir := range []int{4, 8} {
		for index := range 2 {
			exerciseArmJTAGOwnership(t, ir, index)
		}
	}
}

func exerciseArmJTAGOwnership(t *testing.T, ir, index int) {
	t.Helper()
	m, b, cfg := armJTAGFixture(t, ir, index)
	m.ctrl = 0x30000001
	ctx, cancel := context.WithCancel(t.Context())
	c, err := armdebug.Connect(ctx, probe.New(probe.Info{Serial: "jtag"}, b), cfg)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := c.Port().Identity()
	id, present := identity.IDCODE()
	if !ok || !present || id != 0x5ba00477 || b.activations != 1 || c.Info().Serial != "jtag" {
		t.Fatal("managed JTAG identity or activation")
	}
	for ap := range 2 {
		mem, err := c.OpenMemAP(ctx, dap.NewAPSel(uint8(ap)))
		if err != nil {
			t.Fatal(err)
		}
		if value, err := mem.ReadWord(ctx, 0x1000); err != nil || value != uint32(ap+10) {
			t.Fatalf("memory: %x %v", value, err)
		}
	}
	var releases []uint32
	m.onAccept = func(r jtagRequest) {
		if r.ap && !r.read {
			releases = append(releases, m.selectDP>>24)
		}
	}
	b.beforeTransfer = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 25*time.Second || time.Until(deadline) > 30*time.Second {
			t.Fatal("missing JTAG cleanup budget")
		}
		return ctx.Err()
	}
	cancel()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	checkArmJTAGClosed(t, c, b, m, releases)
}

func checkArmJTAGClosed(t *testing.T, c *armdebug.Conn, b *armJTAGBench, m *jtagDPModel, releases []uint32) {
	t.Helper()
	if len(releases) < 2 || releases[0] != 1 || releases[len(releases)-1] != 0 || m.ctrl != 0x30000001 || b.closes != 1 || b.state != jtag.Idle {
		t.Fatalf("release order/control: %v %#x closes=%d state=%v", releases, m.ctrl, b.closes, b.state)
	}
	for _, tap := range b.taps {
		if tap.instruction != (uint64(1)<<tap.ir)-1 {
			t.Fatal("chain not bypassed before close")
		}
	}
	before := b.calls
	if c.Port() != nil || c.Close() != nil || b.calls != before || b.closes != 1 {
		t.Fatal("completed release repeated")
	}
}

func TestArmJTAGFailedSetupAndCloseRetries(t *testing.T) {
	_, b, cfg := armJTAGFixture(t, 4, 0)
	want := errors.New("wire unavailable")
	b.beforeTransfer = func(context.Context) error { return want }
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), cfg)
	if c == nil || !errors.Is(err, want) || b.closes != 0 || c.Port() != nil {
		t.Fatalf("lost setup owner: %v", err)
	}
	b.beforeTransfer = nil
	b.closeErr = errors.New("host close")
	if err := c.Close(); !errors.Is(err, b.closeErr) {
		t.Fatal(err)
	}
	before := b.calls
	b.closeErr = nil
	if err := c.Close(); err != nil || b.calls != before {
		t.Fatalf("repeated DAP release: %v", err)
	}
}

func TestArmJTAGActivationFailureRetainsCleanupOwner(t *testing.T) {
	_, b, cfg := armJTAGFixture(t, 4, 0)
	b.activationErr, b.closeErr = errors.New("activation failed"), errors.New("close failed")
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), cfg)
	if c == nil || !errors.Is(err, b.activationErr) || !errors.Is(err, b.closeErr) || b.calls != 0 {
		t.Fatalf("activation discarded owner: %v", err)
	}
	b.closeErr = nil
	if err := c.Close(); err != nil || b.calls != 0 {
		t.Fatalf("activation cleanup: %v", err)
	}
}

func TestArmJTAGFailedMemoryReleaseRetainsDependencies(t *testing.T) {
	model, b, cfg := armJTAGFixture(t, 4, 0)
	cfg.CleanupTimeout = 4 * time.Second
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), cfg)
	if err != nil {
		t.Fatal(err)
	}
	mem, err := c.OpenMemAP(t.Context(), dap.NewAPSel(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0x1000); err != nil {
		t.Fatal(err)
	}
	writes := 0
	model.onAccept = func(r jtagRequest) {
		if r.ap && !r.read {
			writes++
		}
	}
	want := errors.New("restore unavailable")
	observed := false
	b.beforeTransfer = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !observed && (!ok || time.Until(deadline) < 3*time.Second || time.Until(deadline) > 4*time.Second) {
			t.Fatal("missing custom cleanup budget")
		}
		observed = true
		return want
	}
	if err := c.Close(); !errors.Is(err, want) || b.closes != 0 || c.Port() != nil {
		t.Fatalf("lost memory cleanup: %v", err)
	}
	b.beforeTransfer = nil
	if err := c.Close(); err != nil || b.closes != 1 || writes == 0 {
		t.Fatalf("memory retry: %v", err)
	}
}
