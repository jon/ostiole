package dap_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
)

func jtagRestorationFixture(t *testing.T) (*jtagDPModel, *jtagDPWire, *dap.DebugPort, *dap.MemAP) {
	t.Helper()
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
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dp.Release(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := dp.WriteRawAP(t.Context(), sel.Address(4), 0x88); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	return m, wire, dp, mem
}

func TestJTAGMEMAPReleaseRepairsStickyBeforeAPAcceptance(t *testing.T) {
	m, wire, dp, mem := jtagRestorationFixture(t)
	sel := dap.NewAPSel(1)
	complete := m.complete
	savedCSW := complete(jtagRequest{ap: true, read: true, addr: 0})
	if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	m.complete = func(jtagRequest) uint32 { m.ctrl |= 1 << 5; return 0 }
	m.stuckSticky = true
	if _, err := dp.ReadAPIDR(t.Context(), sel); !errors.Is(err, dap.ErrFault) {
		t.Fatalf("sticky fault: %v", err)
	}
	if m.ctrl&(1<<5) == 0 {
		t.Fatal("fixture did not retain sticky error")
	}
	m.complete, m.stuckSticky = complete, false
	start := len(m.accepted)
	apCount := 0
	m.onAccept = func(r jtagRequest) {
		if r.ap {
			apCount++
			if m.ctrl&(1<<5) != 0 {
				t.Error("AP accepted before sticky repair")
			}
		}
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if apCount == 0 {
		t.Fatal("restoration sent no AP requests")
	}
	requireJTAGStickyRepairBeforeAP(t, m.accepted[start:], m.ctrl)
	before := wire.calls
	if err := mem.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatalf("completed memory release: %v, calls %d -> %d", err, before, wire.calls)
	}
	m.onAccept = nil
	// Read the shared AP model directly after restoration while the DAP remains cleanup-only.
	if got := complete(jtagRequest{ap: true, read: true, addr: 0}); got != savedCSW {
		t.Fatalf("restored CSW=%#x, want %#x", got, savedCSW)
	}
	if got := complete(jtagRequest{ap: true, read: true, addr: 4}); got != 0x88 {
		t.Fatalf("restored TAR=%#x", got)
	}
}

func TestJTAGAPWaitPreservesAbortFailureAndRequiresReentry(t *testing.T) {
	for _, ir := range []int{4, 8} {
		t.Run(fmt.Sprint(ir), func(t *testing.T) { exerciseJTAGFailedAPAbort(t, ir) })
	}
}

func exerciseJTAGFailedAPAbort(t *testing.T, ir int) {
	t.Helper()
	m, wire, chain := jtagModelChain(t, ir, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0), dap.WithMaxWaits(2))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	nativeErr := errors.New("abort completion failed")
	wire.failure = nativeErr
	m.nextWaits = 100
	m.onAbort = func() { wire.failAfter = true }
	start := len(m.accepted)
	_, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
	if !errors.Is(err, dap.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) || !errors.Is(err, nativeErr) || errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("AP wait and failed abort: %v", err)
	}
	before := wire.calls
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || wire.calls != before {
		t.Fatal("ordinary traffic after failed abort")
	}
	if !dp.CleanupPending() {
		t.Fatal("failed abort discarded cleanup")
	}
	m.onAbort = nil
	resets := wire.resets
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dp.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if wire.resets <= resets {
		t.Fatal("cleanup did not re-enter the chain after failed abort")
	}
	accepted := countJTAGAPRequests(m.accepted[start:])
	if accepted != 1 || m.waits != 0 || m.pending != nil && m.pending.ap {
		t.Fatalf("AP requests=%d waits=%d pending=%+v", accepted, m.waits, m.pending)
	}
	before = wire.calls
	if err := dp.Release(ctx); err != nil || wire.calls != before {
		t.Fatal("completed cleanup repeated traffic")
	}
}

func TestJTAGReleaseChecksInheritedOverrunRestoration(t *testing.T) {
	for _, ir := range []int{4, 8} {
		t.Run(fmt.Sprint(ir), func(t *testing.T) {
			m, wire, chain := jtagModelChain(t, ir, 0)
			m.ctrl = 0xf0000001
			dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			if m.ctrl != 0xf0000000 {
				t.Fatalf("connected CTRL/STAT=%#x", m.ctrl)
			}
			m.ignoreControl = true
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := dp.Release(ctx); err == nil {
				t.Fatal("release accepted ignored restoration")
			}
			if !dp.CleanupPending() || m.ctrl != 0xf0000000 {
				t.Fatalf("pending=%t CTRL/STAT=%#x", dp.CleanupPending(), m.ctrl)
			}
			before := wire.calls
			if _, err := dp.ReadDP(ctx, dap.CTRLSTAT); err == nil || wire.calls != before {
				t.Fatal("ordinary traffic while restoration pending")
			}
			m.ignoreControl = false
			if err := dp.Release(ctx); err != nil {
				t.Fatal(err)
			}
			if m.ctrl != 0xf0000001 || dp.CleanupPending() {
				t.Fatalf("restored CTRL/STAT=%#x pending=%t", m.ctrl, dp.CleanupPending())
			}
			before = wire.calls
			if err := dp.Release(ctx); err != nil || wire.calls != before {
				t.Fatal("successful release repeated traffic")
			}
		})
	}
}

func requireJTAGStickyRepairBeforeAP(t *testing.T, requests []jtagRequest, state uint32) {
	t.Helper()
	sawRead, sawClear := false, false
	for _, r := range requests {
		if r.ap {
			break
		}
		if r.addr == 4 {
			if r.read {
				sawRead = true
			} else if r.data&(1<<5) != 0 {
				sawClear = true
			}
		}
	}
	if !sawRead || !sawClear || state&(1<<5) != 0 {
		t.Fatalf("repair before AP: read=%t clear=%t CTRL/STAT=%#x", sawRead, sawClear, state)
	}
}

func countJTAGAPRequests(requests []jtagRequest) int {
	count := 0
	for _, r := range requests {
		if r.ap {
			count++
		}
	}
	return count
}
