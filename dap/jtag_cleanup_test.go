package dap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/jtag"
)

func TestJTAGCancellationOfPendingAPDoesNotReportUnsent(t *testing.T) {
	m, _, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.onWait = cancel
	m.nextWaits = 3
	_, err := dp.ReadAPIDR(ctx, dap.NewAPSel(1))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, dap.ErrIndeterminate) || errors.Is(err, dap.ErrNotExecuted) || errors.Is(err, dap.ErrWait) {
		t.Fatalf("canceled pending AP = %v", err)
	}
	if m.aborts != 1 {
		t.Fatalf("aborts = %d", m.aborts)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGAmbiguousAPAcceptanceIsNeverReplayed(t *testing.T) {
	m, wire, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.onAccept = func(r jtagRequest) {
		if r.ap {
			wire.failAfter = true
		}
	}
	_, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
	if !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("ambiguous AP = %v", err)
	}
	accepted := 0
	for _, r := range m.accepted {
		if r.ap {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d AP requests", accepted)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGChangedChainBlocksMemoryRestorationUntilExactRetry(t *testing.T) {
	m, wire, chain := jtagModelChain(t, 4, 0)
	target := sim.New(0x2ba01477)
	sel := dap.NewAPSel(1)
	if err := target.AddMEMAP(sel, 0x24770011, map[uint32]uint32{0x100: 42}); err != nil {
		t.Fatal(err)
	}
	shareAPModel(t, m, target)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), sel.Address(4), 0x88); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	wire.fail = true
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil {
		t.Fatal("missing scan failure")
	}
	original := wire.taps[1].id
	wire.taps[1].id = 0x14730095
	before := len(m.accepted)
	if err := mem.Release(t.Context()); err == nil {
		t.Fatal("restored through changed chain")
	}
	for _, r := range m.accepted[before:] {
		if r.ap {
			t.Fatal("AP traffic before exact revalidation")
		}
	}
	wire.taps[1].id = original
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	before = len(m.accepted)
	if err := mem.Release(t.Context()); err != nil || len(m.accepted) != before {
		t.Fatal("completed restoration repeated")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

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

func TestJTAGFaultCleanupMustClearStickyState(t *testing.T) {
	m, wire, chain := jtagModelChain(t, 4, 0)
	m.ctrl = 0xf0000000
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.complete = func(jtagRequest) uint32 { m.ctrl |= 1 << 5; return 0 }
	m.stuckSticky = true
	if _, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1)); !errors.Is(err, dap.ErrFault) {
		t.Fatalf("fault = %v", err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil {
		t.Fatal("continued after failed sticky cleanup")
	}
	if err := dp.Release(t.Context()); err == nil {
		t.Fatal("released with sticky cleanup pending")
	}
	m.stuckSticky = false
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.ctrl != 0xf0000000 {
		t.Fatalf("restored CTRL/STAT = %#x", m.ctrl)
	}
	before := wire.calls
	if err := dp.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatal("completed cleanup repeated")
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

func TestJTAGPartialMEMAPRestorationRetainsOnlyFailedRegisters(t *testing.T) {
	m, _, chain := jtagModelChain(t, 4, 0)
	target := sim.New(0x2ba01477)
	sel := dap.NewAPSel(1)
	if err := target.AddMEMAP(sel, 0x24770011, map[uint32]uint32{0x100: 42}); err != nil {
		t.Fatal(err)
	}
	shareAPModel(t, m, target)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	complete := m.complete
	var restored []uint8
	rejectTAR := true
	m.complete = func(r jtagRequest) uint32 {
		if !r.read {
			if r.addr == 4 && rejectTAR {
				rejectTAR = false
				m.ctrl |= 1 << 5
				return 0
			}
			restored = append(restored, r.addr)
		}
		return complete(r)
	}
	if err := mem.Release(t.Context()); !errors.Is(err, dap.ErrFault) {
		t.Fatalf("first restore: %v", err)
	}
	if len(restored) != 1 || restored[0] != 0 {
		t.Fatalf("first restored registers: %v", restored)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 || restored[1] != 4 {
		t.Fatalf("repeated restored registers: %v", restored)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
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
	if _, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(0)); err == nil || wire.calls != before {
		t.Fatal("AP access used an unconfirmed SELECT")
	}
	m.onAccept = nil
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
		if _, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1)); err == nil || wire.calls != before {
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
