package dap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/jtag"
)

func TestJTAGRejectsInheritedTransactionModesWithoutChangingThem(t *testing.T) {
	for _, mode := range []uint32{1 << 2, 2 << 2, 1 << 12} {
		m, _, chain := jtagModelChain(t, 4, 0)
		m.ctrl = mode | 1
		dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
		if _, err := dp.Connect(t.Context()); err == nil {
			t.Fatal("accepted inherited mode")
		}
		if m.ctrl != mode|1 {
			t.Fatalf("inherited state changed to %#x", m.ctrl)
		}
		if err := dp.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJTAGSetupRequiresConfirmedOverrunMode(t *testing.T) {
	m, _, chain := jtagModelChain(t, 4, 0)
	m.ctrl, m.ignoreControl = 0xf0000001, true
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err == nil {
		t.Fatal("connected without disabling ORUNDETECT")
	}
	if m.ctrl != 0xf0000001 {
		t.Fatal("inherited control changed")
	}
}

func TestJTAGStaleTAPRequiresExactReentry(t *testing.T) {
	_, _, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := chain.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); !errors.Is(err, jtag.ErrChainInvalid) {
		t.Fatalf("stale TAP: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGReservedACKInvalidatesProtocol(t *testing.T) {
	for _, ack := range []byte{0, 3, 4, 5, 6, 7} {
		m, wire, chain := jtagModelChain(t, 4, 0)
		dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
		if _, err := dp.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		m.forcedACK = &ack
		if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); !errors.Is(err, dap.ErrProtocol) {
			t.Fatalf("ACK %d: %v", ack, err)
		}
		before := wire.calls
		if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || wire.calls != before {
			t.Fatal("traffic after invalid ACK")
		}
		if err := dp.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJTAGRecoveryDeadlineCanBeRetriedIndependently(t *testing.T) {
	_, wire, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0), dap.WithCleanupTimeout(time.Second))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	wire.fail = true
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil {
		t.Fatal("missing scan failure")
	}
	wire.expire = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := dp.Release(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded revalidation: %v", err)
	}
	if err := dp.Release(ctx); err != nil {
		t.Fatalf("fresh recovery with canceled caller: %v", err)
	}
}

func TestJTAGFailedInitialEntryDoesNotAbortInheritedWork(t *testing.T) {
	m, _, chain := jtagModelChain(t, 4, 0)
	m.pending, m.waits, m.ctrl = &jtagRequest{ap: true, read: true, addr: 12}, 100, 1<<12
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0), dap.WithMaxWaits(2))
	if _, err := dp.Connect(t.Context()); !errors.Is(err, dap.ErrWait) {
		t.Fatalf("inherited WAIT: %v", err)
	}
	if m.aborts != 0 || m.pending == nil || m.ctrl != 1<<12 {
		t.Fatal("entry altered inherited work")
	}
	m.waits = 0
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.aborts != 0 || m.ctrl != 1<<12 {
		t.Fatal("cleanup aborted inherited work")
	}
}

func TestJTAGIncompleteSELECTBlocksOrdinaryTraffic(t *testing.T) {
	m, wire, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.onAccept = func(r jtagRequest) {
		if !r.ap && !r.read && r.addr == 8 {
			cancel()
		}
	}
	if err := dp.WriteDP(ctx, dap.SELECT, 1<<24); !errors.Is(err, context.Canceled) {
		t.Fatalf("SELECT: %v", err)
	}
	before := wire.calls
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || wire.calls != before {
		t.Fatal("DP access continued after an unconfirmed SELECT")
	}
	m.onAccept = nil
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
