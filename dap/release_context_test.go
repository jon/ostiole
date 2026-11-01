package dap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type releaseContextWire struct {
	inner   swd.Wire
	fail    bool
	calls   int
	observe func(context.Context, int)
}

func (w *releaseContextWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	if w.observe != nil {
		w.observe(ctx, bits)
	}
	if w.fail {
		w.fail = false
		return nil, errors.New("injected wire failure")
	}
	return w.inner.SWDIO(ctx, direction, output, bits)
}

func brokenMEMAPForRelease(t *testing.T) (*dap.DebugPort, *dap.MemAP, *releaseContextWire, [2]uint32) {
	t.Helper()
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, nil)
	wire := &releaseContextWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)), dap.WithCleanupTimeout(3*time.Second))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved := readReleaseRegisters(t, dp)
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadScalar(t.Context(), 0x100, dap.Size32); err != nil {
		t.Fatal(err)
	}
	wire.fail = true
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil {
		t.Fatal("missing wire failure")
	}
	return dp, mem, wire, saved
}

func readReleaseRegisters(t *testing.T, dp *dap.DebugPort) [2]uint32 {
	t.Helper()
	var values [2]uint32
	for i, reg := range []uint16{0, 4} {
		value, err := dp.ReadRawAP(t.Context(), apSel(0).Address(reg))
		if err != nil {
			t.Fatal(err)
		}
		values[i] = value
	}
	return values
}

func TestMEMAPReleaseReentryUsesCallerContext(t *testing.T) {
	for _, mode := range []string{"canceled", "expired", "during-entry", "live"} {
		t.Run(mode, func(t *testing.T) { checkMEMAPReleaseContext(t, mode) })
	}
}

func releaseTestContext(t *testing.T, mode string) (context.Context, context.CancelFunc) {
	t.Helper()
	switch mode {
	case "canceled":
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		return ctx, cancel
	case "expired":
		return context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	default:
		return context.WithTimeout(t.Context(), 500*time.Millisecond)
	}
}

func checkMEMAPReleaseContext(t *testing.T, mode string) {
	t.Helper()
	dp, mem, wire, saved := brokenMEMAPForRelease(t)
	ctx, cancel := releaseTestContext(t, mode)
	defer cancel()
	before := wire.calls
	entryCanceled := false
	wire.observe = observeReleaseContext(t, mode, ctx, cancel, &entryCanceled)
	err := mem.Release(ctx)
	switch mode {
	case "live":
		if err != nil || wire.calls == before {
			t.Fatalf("release: %v; no restoration traffic: %v", err, wire.calls == before)
		}
	case "during-entry":
		if !entryCanceled || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation during entry: %v", err)
		}
	default:
		if !errors.Is(err, ctx.Err()) || wire.calls != before {
			t.Fatalf("preflight release: %v; calls %d -> %d", err, before, wire.calls)
		}
	}
	wire.observe = nil
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := readReleaseRegisters(t, dp); got != saved {
		t.Fatalf("restored CSW/TAR = %#x; want %#x", got, saved)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNestedSWDConnectCleanupUsesDAPBudget(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, nil)
	wire := &releaseContextWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)), dap.WithCleanupTimeout(4*time.Second))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadScalar(t.Context(), 0x100, dap.Size32); err != nil {
		t.Fatal(err)
	}
	wire.fail = true
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil {
		t.Fatal("missing wire failure")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	entries := 0
	wire.fail = true
	wire.observe = func(got context.Context, bits int) {
		if bits != 136 {
			return
		}
		entries++
		if entries == 1 {
			return
		}
		deadline, ok := got.Deadline()
		if !ok || time.Until(deadline) < 3*time.Second || time.Until(deadline) > 4*time.Second {
			t.Errorf("nested cleanup budget: %v, deadline present %v", time.Until(deadline), ok)
		}
		if got == ctx || got.Err() != nil {
			t.Error("nested recovery did not use independent live context")
		}
	}
	if err := mem.Release(ctx); err == nil {
		t.Fatal("missing failed reentry")
	}
	if entries < 2 {
		t.Fatalf("nested SWD cleanup not exercised: %d entries", entries)
	}
	wire.observe = nil
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.OverrunDetectEnabled() {
		t.Fatal("original ORUNDETECT not restored")
	}
}

func assertCanceledMEMAPRelease(t *testing.T, mem *dap.MemAP, ctx context.Context) {
	t.Helper()
	if err := mem.Release(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Release: %v", err)
	}
}

func observeReleaseContext(t *testing.T, mode string, ctx context.Context, cancel context.CancelFunc, entryCanceled *bool) func(context.Context, int) {
	t.Helper()
	return func(got context.Context, bits int) {
		if mode == "during-entry" {
			if !*entryCanceled && bits == 136 {
				if got != ctx {
					t.Error("entry substituted caller context")
				}
				*entryCanceled = true
				cancel()
			}
			return
		}
		if got != ctx {
			t.Error("release substituted caller context")
		}
	}
}
