package sim_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

func newSelectionConnection(t *testing.T, overrun bool) (*swd.Conn, *connectionTarget, *packedCountingWire) {
	t.Helper()
	target := &connectionTarget{
		dpidr: 0x2ba01477, ctrlStat: 0x50000000,
		ignoreORUN: !overrun, enforceSticky: true,
	}
	if overrun {
		target.ctrlStat |= testOverrunDetect
	}
	wire := &packedCountingWire{inner: sim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := conn.Release(ctx); err != nil {
			t.Error(err)
		}
	})
	return conn, target, wire
}

func selectionRead(t *testing.T, conn *swd.Conn, batch bool, addr uint8) (uint32, error) {
	t.Helper()
	if !batch {
		return conn.ReadDP(t.Context(), addr)
	}
	queued := conn.NewBatch()
	result := queued.ReadDP(addr)
	err := queued.Commit(t.Context())
	value, resultErr := result.Value()
	if !errors.Is(err, resultErr) {
		t.Fatalf("Commit = %v, result error = %v", err, resultErr)
	}
	return value, err
}

func assertSelectionWriteBlocked(t *testing.T, conn *swd.Conn, target *connectionTarget, wire *packedCountingWire) {
	t.Helper()
	calls, writes, state := len(wire.calls), target.ctrlWrites, target.ctrlStat
	if err := conn.WriteDP(t.Context(), 4, 0); err == nil {
		t.Fatal("address-0x04 write succeeded with an unresolved bank")
	}
	if len(wire.calls) != calls || target.ctrlWrites != writes || target.ctrlStat != state {
		t.Fatal("rejected address-0x04 write reached the wire or changed CTRL/STAT")
	}
}

func assertSelectionRecovery(t *testing.T, conn *swd.Conn, target *connectionTarget) {
	t.Helper()
	state := target.ctrlStat & 0x50000001
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteDP(t.Context(), 8, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteDP(t.Context(), 4, 0x55); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	if target.dlcr != 0x55 || target.ctrlStat&0x50000001 != state {
		t.Fatalf("DLCR=%#x CTRL/STAT=%#x, want DLCR=0x55 with control bits %#x", target.dlcr, target.ctrlStat, state)
	}
}

func TestPendingSELECTExemptReads(t *testing.T) {
	for _, overrun := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			for _, read := range []struct {
				name    string
				addr    uint8
				unknown bool
			}{
				{name: "DPIDR", addr: 0},
				{name: "old CTRLSTAT", addr: 4},
				{name: "unknown old CTRLSTAT", addr: 4, unknown: true},
			} {
				t.Run(fmt.Sprintf("overrun=%t/batch=%t/%s", overrun, batch, read.name), func(t *testing.T) {
					checkPendingSELECTExemptRead(t, overrun, batch, read.addr, read.unknown)
				})
			}
		}
	}
}

func checkPendingSELECTExemptRead(t *testing.T, overrun, batch bool, addr uint8, unknown bool) {
	t.Helper()
	conn, target, wire := newSelectionConnection(t, overrun)
	if unknown {
		if err := conn.WriteDP(t.Context(), 8, 0); err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteDP(t.Context(), 0, 1<<3); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.WriteDP(t.Context(), 8, 1); err != nil {
		t.Fatal(err)
	}
	target.pendingDP = nil
	target.ctrlStat |= testWriteDataErr
	want := target.dpidr
	if addr == 4 {
		want = target.ctrlStat
	}
	if value, err := selectionRead(t, conn, batch, addr); err != nil || value != want {
		t.Fatalf("read = %#x, %v; want %#x", value, err, want)
	}
	assertSelectionWriteBlocked(t, conn, target, wire)
	if err := conn.WriteDP(t.Context(), 0, 1<<3); err != nil {
		t.Fatal(err)
	}
	if _, err := selectionRead(t, conn, batch, 12); err != nil {
		t.Fatal(err)
	}
	assertSelectionWriteBlocked(t, conn, target, wire)
	assertSelectionRecovery(t, conn, target)
}

func TestPendingSELECTWAIT(t *testing.T) {
	for _, overrun := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("overrun=%t/batch=%t", overrun, batch), func(t *testing.T) {
				checkPendingSELECTWAIT(t, overrun, batch)
			})
		}
	}
}

func checkPendingSELECTWAIT(t *testing.T, overrun, batch bool) {
	t.Helper()
	conn, target, wire := newSelectionConnection(t, overrun)
	target.waitRDBUFF = 1
	before := len(wire.calls)
	var err error
	if batch {
		queued := conn.NewBatch()
		queued.WriteDP(8, 1)
		queued.ReadDP(12)
		err = queued.Commit(t.Context())
	} else {
		if err = conn.WriteDP(t.Context(), 8, 1); err != nil {
			t.Fatal(err)
		}
		_, err = conn.ReadDP(t.Context(), 12)
	}
	if !errors.Is(err, swd.ErrWait) {
		t.Fatalf("completion = %v, want WAIT", err)
	}
	if overrun && batch && !slices.Equal(wire.calls[before:], []int{108, 54}) {
		t.Fatalf("WAIT traffic = %v, want a packed SELECT/read followed by ABORT", wire.calls[before:])
	}
	assertSelectionWriteBlocked(t, conn, target, wire)
	if overrun {
		before := len(wire.calls)
		if _, err := conn.ReadDP(t.Context(), 0); err == nil || len(wire.calls) != before {
			t.Fatalf("DPIDR after WAIT = %v, wire calls = %d; want repair without traffic", err, len(wire.calls)-before)
		}
	} else {
		if err := conn.WriteDP(t.Context(), 0, 1<<3); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ReadDP(t.Context(), 12); err != nil {
			t.Fatal(err)
		}
		assertSelectionWriteBlocked(t, conn, target, wire)
	}
	assertSelectionRecovery(t, conn, target)
}

func TestPendingSELECTABORT(t *testing.T) {
	for _, overrun := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("overrun=%t/batch=%t", overrun, batch), func(t *testing.T) {
				conn, target, wire := newSelectionConnection(t, overrun)
				before := len(wire.calls)
				discardSelection(t, conn, batch)
				if overrun && batch && !slices.Equal(wire.calls[before:], []int{162}) {
					t.Fatalf("ABORT traffic = %v, want packed SELECT/ABORT/RDBUFF", wire.calls[before:])
				}
				assertSelectionWriteBlocked(t, conn, target, wire)
				assertSelectionRecovery(t, conn, target)
			})
		}
	}
}

func discardSelection(t *testing.T, conn *swd.Conn, batch bool) {
	t.Helper()
	if batch {
		queued := conn.NewBatch()
		queued.WriteDP(8, 1)
		queued.WriteDP(0, 1<<3)
		queued.ReadDP(12)
		if err := queued.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := conn.WriteDP(t.Context(), 8, 1); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteDP(t.Context(), 0, 1<<3); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
}

func TestPendingSELECTSettlesThroughRDBUFF(t *testing.T) {
	for _, overrun := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("overrun=%t/batch=%t", overrun, batch), func(t *testing.T) {
				checkSELECTCompletion(t, overrun, batch)
			})
		}
	}
}

func checkSELECTCompletion(t *testing.T, overrun, batch bool) {
	t.Helper()
	conn, target, wire := newSelectionConnection(t, overrun)
	state := target.ctrlStat
	if err := conn.WriteDP(t.Context(), 8, 1); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []uint8{0, 4} {
		if _, err := selectionRead(t, conn, batch, addr); err != nil {
			t.Fatal(err)
		}
		assertSelectionWriteBlocked(t, conn, target, wire)
	}
	if _, err := selectionRead(t, conn, batch, 12); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteDP(t.Context(), 4, 0x55); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	if target.dlcr != 0x55 || target.ctrlStat != state {
		t.Fatalf("DLCR=%#x CTRL/STAT=%#x, want 0x55 and %#x", target.dlcr, target.ctrlStat, state)
	}
}

func TestBatchRejectsUnsettledSELECTBeforeTraffic(t *testing.T) {
	for _, overrun := range []bool{false, true} {
		t.Run(fmt.Sprint(overrun), func(t *testing.T) {
			conn, _, wire := newSelectionConnection(t, overrun)
			before := len(wire.calls)
			queued := conn.NewBatch()
			queued.WriteDP(8, 1)
			queued.ReadDP(4)
			write := queued.WriteDP(4, 0)
			if err := queued.Commit(t.Context()); err == nil || len(wire.calls) != before {
				t.Fatalf("Commit = %v, wire calls = %d; want rejection without traffic", err, len(wire.calls)-before)
			}
			if err := write.Err(); err == nil || errors.Is(err, swd.ErrIndeterminate) {
				t.Fatalf("write = %v, want determinate rejection", err)
			}
		})
	}
}

func TestBatchSplitsPossibleCTRLSTATAfterSELECT(t *testing.T) {
	conn, target, wire := newSelectionConnection(t, true)
	if err := wire.inner.SetMaxTransferBits(162); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteDP(t.Context(), 8, 1); err != nil {
		t.Fatal(err)
	}
	target.pendingDP = nil
	target.ctrlStat |= testWriteDataErr
	before, writes, state := len(wire.calls), target.ctrlWrites, target.ctrlStat
	queued := conn.NewBatch()
	ctrl := queued.ReadDP(4)
	queued.ReadDP(12)
	queued.WriteDP(4, 0)
	if err := queued.Commit(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Commit = %v, want FAULT", err)
	}
	if value, err := ctrl.Value(); err != nil || value != state {
		t.Fatalf("CTRL/STAT = %#x, %v; want %#x", value, err, state)
	}
	if !slices.Equal(wire.calls[before:], []int{54, 108}) || target.ctrlWrites != writes {
		t.Fatalf("calls=%v CTRL writes=%d, want [54 108] with no new writes", wire.calls[before:], target.ctrlWrites-writes)
	}
}
