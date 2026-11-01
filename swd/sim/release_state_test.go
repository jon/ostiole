package sim_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

func cleanupConnection(t *testing.T, conn *swd.Conn) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := conn.Release(ctx); err != nil {
			t.Error(err)
		}
	})
}

func TestReconnectRestoresInitiallyInheritedOverrun(t *testing.T) {
	for _, original := range []uint32{0, testOverrunDetect} {
		t.Run(fmt.Sprint(original), func(t *testing.T) {
			target := &connectionTarget{dpidr: 0x2ba01477, ctrlStat: original, enforceSticky: true}
			wire := &countingWire{wire: sim.New(target)}
			conn := swd.New(wire)
			cleanupConnection(t, conn)
			for range 2 {
				if _, err := conn.Connect(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if err := conn.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if target.ctrlStat&testOverrunDetect != original || target.selectDP != 0 || target.pendingDP != nil {
				t.Errorf("released CTRL/STAT=%#x SELECT=%#x pending=%v, want ORUNDETECT=%#x", target.ctrlStat, target.selectDP, target.pendingDP, original)
			}
			assertIdleReleaseSendsNoTraffic(t, conn, wire)
		})
	}
}

func TestReleaseRepairsUnknownBankOnReadyConnection(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, enforceSticky: true}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)
	cleanupConnection(t, conn)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	selectReleaseTestBank(t, conn, target)
	if err := conn.WriteDP(t.Context(), 8, 2); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteDP(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 0); err != nil {
		t.Fatalf("connection was not Ready after ABORT: %v", err)
	}
	target.resetSticky = false
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !target.resetSticky || target.selectDP != 0 || target.ctrlStat&testOverrunDetect != 0 {
		t.Errorf("unknown-bank release reset=%t SELECT=%#x CTRL/STAT=%#x", target.resetSticky, target.selectDP, target.ctrlStat)
	}
	assertIdleReleaseSendsNoTraffic(t, conn, wire)
}

func TestReleaseSelectsKnownNonzeroBankWithoutChangingDLCR(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, enforceSticky: true}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)
	cleanupConnection(t, conn)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.dlcr = 0x44
	selectReleaseTestBank(t, conn, target)
	target.resetSticky = false
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.resetSticky || target.selectDP != 0 || target.dlcr != 0x44 || target.pendingDP != nil || target.ctrlStat&testOverrunDetect != 0 {
		t.Errorf("known-bank release reset=%t SELECT=%#x DLCR=%#x pending=%v CTRL/STAT=%#x", target.resetSticky, target.selectDP, target.dlcr, target.pendingDP, target.ctrlStat)
	}
	assertIdleReleaseSendsNoTraffic(t, conn, wire)
}

func selectReleaseTestBank(t *testing.T, conn *swd.Conn, target *connectionTarget) {
	t.Helper()
	if err := conn.WriteDP(t.Context(), 8, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	if target.selectDP != 1 || target.pendingDP != nil {
		t.Fatalf("SELECT not independently settled: %#x, %v", target.selectDP, target.pendingDP)
	}
}

func TestReleaseClearsNonOverrunStickyBeforeRestoringControl(t *testing.T) {
	for _, flag := range []uint32{testStickyCompare, testStickyError, testWriteDataErr, testStickyCompare | testStickyError | testWriteDataErr} {
		t.Run(fmt.Sprintf("%x", flag), func(t *testing.T) {
			target := &connectionTarget{dpidr: 0x2ba01477, enforceSticky: true}
			wire := &countingWire{wire: sim.New(target)}
			conn := swd.New(wire)
			cleanupConnection(t, conn)
			if _, err := conn.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			target.ctrlStat |= flag
			if err := conn.Release(t.Context()); err != nil {
				t.Errorf("first Release with sticky %#x failed: %v", flag, err)
			}
			if target.ctrlStat&(testOverrunDetect|testStickyOverrun|testStickyCompare|testStickyError|testWriteDataErr) != 0 {
				t.Errorf("released CTRL/STAT=%#x, want acquired and sticky bits clear", target.ctrlStat)
			}
			assertIdleReleaseSendsNoTraffic(t, conn, wire)
		})
	}
}

func TestOverrunTransitionFaultAfterAbandonedControlWrite(t *testing.T) {
	for _, enabling := range []bool{true, false} {
		t.Run(fmt.Sprint(enabling), func(t *testing.T) {
			target := &connectionTarget{dpidr: 0x2ba01477, enforceSticky: true, faultORUN: enabling}
			wire := &countingWire{wire: sim.New(target)}
			conn := swd.New(wire)
			cleanupConnection(t, conn)
			if _, err := conn.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := wire.calls
			if got, err := conn.ReadDP(t.Context(), 0); err != nil || got != target.dpidr {
				t.Fatalf("identity under negotiated grammar = %#x, %v", got, err)
			}
			wantCalls := 1
			if enabling {
				wantCalls = 2
			}
			if wire.calls-before != wantCalls || target.ctrlStat&(testStickyOverrun|testWriteDataErr) != 0 {
				t.Errorf("grammar used %d calls, want %d; CTRL/STAT=%#x", wire.calls-before, wantCalls, target.ctrlStat)
			}
			if !enabling {
				target.faultORUN = true
			}
			if err := conn.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			wantWrites := 1
			if !enabling {
				wantWrites = 3
			}
			if target.ctrlWrites != wantWrites || target.ctrlStat&(testOverrunDetect|testStickyOverrun|testWriteDataErr) != 0 {
				t.Errorf("CTRL writes=%d want=%d, released CTRL/STAT=%#x", target.ctrlWrites, wantWrites, target.ctrlStat)
			}
			assertIdleReleaseSendsNoTraffic(t, conn, wire)
		})
	}
}

func assertIdleReleaseSendsNoTraffic(t *testing.T, conn *swd.Conn, wire *countingWire) {
	t.Helper()
	before := wire.calls
	if err := conn.Release(t.Context()); err != nil || wire.calls != before {
		t.Errorf("idle Release = %v, traffic delta %d", err, wire.calls-before)
	}
}

func TestReleaseRetainsRestorationAfterStickyClearFailure(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, enforceSticky: true}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)
	cleanupConnection(t, conn)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	clearErr := errors.New("sticky clear transfer failed")
	target.ctrlStat |= testStickyError
	target.abortErr = clearErr
	if err := conn.Release(t.Context()); !errors.Is(err, clearErr) {
		t.Fatalf("first Release = %v, want sticky-clear failure", err)
	}
	if target.ctrlStat&(testOverrunDetect|testStickyError) != testOverrunDetect|testStickyError {
		t.Fatalf("failed clear lost retained state: CTRL/STAT=%#x", target.ctrlStat)
	}
	before := wire.calls
	if _, err := conn.ReadDP(t.Context(), 0); !errors.Is(err, swd.ErrNotExecuted) || wire.calls != before {
		t.Fatalf("cleanup-only read = %v, traffic delta %d", err, wire.calls-before)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.ctrlStat&(testOverrunDetect|testStickyError|testStickyOverrun) != 0 {
		t.Errorf("restoration retry left CTRL/STAT=%#x", target.ctrlStat)
	}
	assertIdleReleaseSendsNoTraffic(t, conn, wire)
}

func TestMinimalDPStickyCleanupOmitsCompareClear(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba11477, enforceSticky: true}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)
	cleanupConnection(t, conn)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	// An unexpected reserved status bit does not authorize an unsupported clear.
	target.ctrlStat |= testStickyCompare | testStickyError | testWriteDataErr | testStickyOverrun
	before := len(target.abortWrites)
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := target.abortWrites[before:]; len(got) != 1 || got[0] != 0x1c {
		t.Errorf("minimal-DP sticky clear writes=%#v, want only 0x1c", got)
	}
	if target.ctrlStat != testStickyCompare {
		t.Errorf("minimal-DP final CTRL/STAT=%#x, want unexpected reserved bit preserved", target.ctrlStat)
	}
	assertIdleReleaseSendsNoTraffic(t, conn, wire)
}
