package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

const (
	testOverrunDetect = uint32(1 << 0)
	testStickyOverrun = uint32(1 << 1)
	testWriteDataErr  = uint32(1 << 7)
)

type connectionTarget struct {
	dpidr         uint32
	ctrlStat      uint32
	dlcr          uint32
	selectDP      uint32
	pendingDP     *pendingDPWrite
	waitRDBUFF    int
	waitAP        int
	waitORUN      bool
	ignoreORUN    bool
	abortWrites   []uint32
	ctrlReads     int
	ctrlWrites    int
	failCTRLAt    int
	ctrlReadErr   error
	resetSticky   bool
	enforceSticky bool
}

type pendingDPWrite struct {
	addr  uint8
	bank  uint8
	value uint32
}

func (t *connectionTarget) Acknowledge(_ context.Context, req sim.Request) error {
	if t.enforceSticky && t.stickyFaults(req) {
		return swd.ErrFault
	}
	if req.AP && t.waitAP > 0 {
		t.waitAP--
		return swd.ErrWait
	}
	if !req.AP && req.Read && req.Addr == 0x0c && t.waitORUN && t.pendingDP != nil && t.pendingDP.addr == 0x04 {
		t.waitORUN = false
		return swd.ErrWait
	}
	if !req.AP && req.Read && req.Addr == 0x0c && t.waitRDBUFF > 0 {
		t.waitRDBUFF--
		return swd.ErrWait
	}
	return nil
}

func (t *connectionTarget) stickyFaults(req sim.Request) bool {
	sticky := t.ctrlStat&(testStickyOverrun|testWriteDataErr) != 0
	exempt := !req.AP && (!req.Read && req.Addr == 0x00 || req.Read && (req.Addr == 0x00 || req.Addr == 0x04 && t.selectDP&0x0f == 0))
	return sticky && !exempt
}

func (t *connectionTarget) Read(_ context.Context, req sim.Request) (uint32, error) {
	if req.AP {
		return 0, errors.New("unexpected AP read")
	}
	switch req.Addr {
	case 0x00:
		return t.dpidr, nil
	case 0x04:
		t.ctrlReads++
		if t.failCTRLAt != 0 && t.ctrlReads == t.failCTRLAt {
			return 0, t.ctrlReadErr
		}
		if t.selectDP&0x0f == 1 {
			return t.dlcr, nil
		}
		return t.ctrlStat, nil
	case 0x0c:
		t.applyPending()
		return 0, nil
	default:
		return 0, errors.New("unexpected DP read")
	}
}

func (t *connectionTarget) Write(_ context.Context, req sim.Request, value uint32) error {
	if req.AP {
		return errors.New("unexpected AP write")
	}
	switch req.Addr {
	case 0x00:
		t.abortWrites = append(t.abortWrites, value)
		if t.pendingDP != nil {
			t.pendingDP = nil
			t.ctrlStat |= testWriteDataErr
		}
		if value&(1<<3) != 0 {
			t.ctrlStat &^= testWriteDataErr
		}
		if value&(1<<4) != 0 {
			t.ctrlStat &^= testStickyOverrun
		}
		return nil
	case 0x04, 0x08:
		if req.Addr == 0x04 {
			t.ctrlWrites++
		}
		t.pendingDP = &pendingDPWrite{addr: req.Addr, bank: uint8(t.selectDP & 0x0f), value: value}
		return nil
	default:
		return errors.New("unexpected DP write")
	}
}

func (t *connectionTarget) OverrunDetectEnabled() bool {
	return t.ctrlStat&testOverrunDetect != 0
}

func (t *connectionTarget) ObserveResponse(err error) {
	if err != nil && t.OverrunDetectEnabled() {
		t.ctrlStat |= testStickyOverrun
	}
}

func (t *connectionTarget) ObserveLineReset() {
	t.dlcr = 0
	if t.OverrunDetectEnabled() {
		t.ctrlStat |= testStickyOverrun
		t.resetSticky = true
	}
}

func (t *connectionTarget) applyPending() {
	if t.pendingDP == nil {
		return
	}
	write := *t.pendingDP
	t.pendingDP = nil
	switch write.addr {
	case 0x04:
		if write.bank == 1 {
			t.dlcr = write.value
			return
		}
		if t.ignoreORUN {
			write.value = write.value&^testOverrunDetect | t.ctrlStat&testOverrunDetect
		}
		t.ctrlStat = write.value
	case 0x08:
		t.selectDP = write.value
	}
}

type countingWire struct {
	wire  swd.Wire
	calls int
}

type entryFailureWire struct {
	wire    swd.Wire
	err     error
	entries int
}

func (w *entryFailureWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if bits == 136 {
		w.entries++
		if w.entries == 2 {
			return nil, w.err
		}
	}
	return w.wire.SWDIO(ctx, direction, output, bits)
}

func (w *countingWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	return w.wire.SWDIO(ctx, direction, output, bits)
}

func TestConnectionAcquiresAndReleasesOverrunDetection(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, selectDP: 3}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)

	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil || wire.calls != 0 {
		t.Fatalf("ReadDP() before Connect error = %v after %d wire calls", err, wire.calls)
	}
	dpidr, err := conn.Connect(t.Context())
	if err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	if dpidr != target.dpidr || target.ctrlStat&testOverrunDetect == 0 || target.selectDP != 0 {
		t.Fatalf("Connect() = %#08x, CTRL/STAT=%#08x SELECT=%#08x", dpidr, target.ctrlStat, target.selectDP)
	}

	before := wire.calls
	if _, err := conn.ReadDP(t.Context(), 0x00); err != nil {
		t.Fatalf("ReadDP() after Connect: %v", err)
	}
	if got := wire.calls - before; got != 1 {
		t.Fatalf("overrun ReadDP() used %d SWDIO calls, want 1", got)
	}

	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("Release(): %v", err)
	}
	if target.ctrlStat&testOverrunDetect != 0 {
		t.Fatalf("CTRL/STAT after Release = %#08x, want ORUNDETECT clear", target.ctrlStat)
	}
	before = wire.calls
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil || wire.calls != before {
		t.Fatalf("ReadDP() after Release error = %v after %d new calls", err, wire.calls-before)
	}
}

func TestConnectionPreservesInheritedOverrunDetection(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, ctrlStat: testOverrunDetect}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	if !target.resetSticky || target.ctrlStat&testStickyOverrun != 0 {
		t.Fatalf("line reset sticky observed=%t CTRL/STAT=%#08x, want STICKYORUN cleared during bootstrap", target.resetSticky, target.ctrlStat)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("Release(): %v", err)
	}
	if target.ctrlStat&testOverrunDetect == 0 {
		t.Fatalf("CTRL/STAT after Release = %#08x, want inherited ORUNDETECT", target.ctrlStat)
	}
}

func TestConnectionFallsBackWhenOverrunDetectionDoesNotReadBack(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, ignoreORUN: true}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	before := wire.calls
	if _, err := conn.ReadDP(t.Context(), 0x00); err != nil {
		t.Fatalf("ReadDP(): %v", err)
	}
	if got := wire.calls - before; got != 2 {
		t.Fatalf("simple ReadDP() used %d SWDIO calls, want 2", got)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("Release(): %v", err)
	}
}

func TestConnectionRepeatsOnlyRDBUFFWhileEnablingOverrunDetection(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, waitORUN: true}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	if target.ctrlWrites != 1 {
		t.Fatalf("CTRL/STAT writes = %d, want 1", target.ctrlWrites)
	}
	if target.ctrlStat&testOverrunDetect == 0 {
		t.Fatalf("CTRL/STAT after Connect = %#08x, want ORUNDETECT", target.ctrlStat)
	}
}

func TestConnectionClearsStickyOverrunAfterWait(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, ctrlStat: testOverrunDetect}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	target.waitRDBUFF = 1
	if _, err := conn.ReadDP(t.Context(), 0x0c); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("ReadDP(RDBUFF) error = %v, want WAIT", err)
	}
	if target.ctrlStat&testStickyOverrun != 0 {
		t.Fatalf("CTRL/STAT after WAIT = %#08x, want STICKYORUN clear", target.ctrlStat)
	}
	if len(target.abortWrites) == 0 {
		t.Fatal("WAIT did not issue an ABORT cleanup write")
	}
}

func TestConnectionRequiresRepairWhenWaitCleanupAbandonsSELECT(t *testing.T) {
	for _, test := range []struct {
		name string
		ap   bool
	}{
		{name: "RDBUFF"},
		{name: "AP request", ap: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := &connectionTarget{dpidr: 0x2ba01477}
			wire := &countingWire{wire: sim.New(target)}
			conn := swd.New(wire)
			if _, err := conn.Connect(t.Context()); err != nil {
				t.Fatalf("Connect(): %v", err)
			}
			if err := conn.WriteDP(t.Context(), 0x08, 1); err != nil {
				t.Fatalf("WriteDP(SELECT): %v", err)
			}
			var err error
			if test.ap {
				target.waitAP = 1
				_, err = conn.ReadAP(t.Context(), 0x00)
			} else {
				target.waitRDBUFF = 1
				_, err = conn.ReadDP(t.Context(), 0x0c)
			}
			if !errors.Is(err, swd.ErrWait) {
				t.Fatalf("WAITed request error = %v, want WAIT", err)
			}
			before := wire.calls
			if _, err := conn.ReadDP(t.Context(), 0x00); err == nil || wire.calls != before {
				t.Fatalf("ReadDP() after abandoned SELECT error = %v after %d new calls", err, wire.calls-before)
			}
			if err := conn.Release(t.Context()); err != nil {
				t.Fatalf("Release(): %v", err)
			}
		})
	}
}

func TestConnectionRetriesReleaseAfterOverrunWaitAbandonsDisable(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	target.waitRDBUFF = 1
	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("Release(): %v", err)
	}
	if target.ctrlStat&(testOverrunDetect|testStickyOverrun|testWriteDataErr) != 0 {
		t.Fatalf("CTRL/STAT after Release = %#08x, want acquired and sticky bits clear", target.ctrlStat)
	}
}

func TestConnectionRejectsDirectOverrunChangeBeforeTraffic(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	before := wire.calls
	if err := conn.WriteDP(t.Context(), 0x04, 0); err == nil || wire.calls != before {
		t.Fatalf("WriteDP(CTRL/STAT) error = %v after %d new calls", err, wire.calls-before)
	}
}

func TestConnectionTracksDebugPortBankBeforeAddressFourWrites(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477, dlcr: 0x55}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	if err := conn.WriteDP(t.Context(), 0x08, 1); err != nil {
		t.Fatalf("WriteDP(SELECT): %v", err)
	}
	before := wire.calls
	if err := conn.WriteDP(t.Context(), 0x04, 0); err == nil || wire.calls != before {
		t.Fatalf("WriteDP() with pending SELECT error = %v after %d new calls", err, wire.calls-before)
	}
	if _, err := conn.ReadDP(t.Context(), 0x0c); err != nil {
		t.Fatalf("ReadDP(RDBUFF): %v", err)
	}
	if got, err := conn.ReadDP(t.Context(), 0x04); err != nil || got != target.dlcr {
		t.Fatalf("ReadDP(DLCR) = %#08x, %v; want %#08x", got, err, target.dlcr)
	}
	if err := conn.WriteDP(t.Context(), 0x04, 0); err != nil {
		t.Fatalf("WriteDP(DLCR): %v", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x0c); err != nil {
		t.Fatalf("settle DLCR write: %v", err)
	}
	if target.dlcr != 0 {
		t.Fatalf("DLCR = %#08x, want 0", target.dlcr)
	}
	before = wire.calls
	if err := conn.WriteDP(t.Context(), 0x04, 1<<8); err == nil || wire.calls != before {
		t.Fatalf("turnaround WriteDP(DLCR) error = %v after %d new calls", err, wire.calls-before)
	}
}

func TestProtocolEntryRequiresConnectionRepair(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	if err := conn.JTAGToSWD(t.Context()); err != nil {
		t.Fatalf("JTAGToSWD(): %v", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded after direct protocol entry")
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("Release() after direct protocol entry: %v", err)
	}
	if target.ctrlStat&testOverrunDetect != 0 {
		t.Fatalf("CTRL/STAT after Release = %#08x, want ORUNDETECT clear", target.ctrlStat)
	}
}

func TestProtocolEntryDoesNotCreateCleanupForAnIdleConnection(t *testing.T) {
	for _, entry := range []struct {
		name string
		run  func(*swd.Conn) error
	}{
		{name: "line reset", run: func(conn *swd.Conn) error { return conn.LineReset(t.Context()) }},
		{name: "JTAG to SWD", run: func(conn *swd.Conn) error { return conn.JTAGToSWD(t.Context()) }},
	} {
		for _, state := range []string{"new", "released"} {
			t.Run(entry.name+"/"+state, func(t *testing.T) {
				target := &connectionTarget{dpidr: 0x2ba01477}
				wire := &countingWire{wire: sim.New(target)}
				conn := swd.New(wire)
				if state == "released" {
					if _, err := conn.Connect(t.Context()); err != nil {
						t.Fatalf("Connect(): %v", err)
					}
					if err := conn.Release(t.Context()); err != nil {
						t.Fatalf("Release(): %v", err)
					}
				}
				if err := entry.run(conn); err != nil {
					t.Fatalf("protocol entry: %v", err)
				}
				before := wire.calls
				if err := conn.Release(t.Context()); err != nil {
					t.Fatalf("Release() after idle protocol entry: %v", err)
				}
				if wire.calls != before {
					t.Fatalf("Release() after idle protocol entry sent %d wire calls", wire.calls-before)
				}
			})
		}
	}
}

func TestConnectionRejectsChangedIdentityBeforeRestoringState(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	target.dpidr = 0x0ba01477
	if _, err := conn.Connect(t.Context()); err == nil {
		t.Fatal("Connect() accepted a changed DPIDR")
	}
}

func TestConnectionRejectsInvalidDPIDRBeforeConfiguration(t *testing.T) {
	target := &connectionTarget{}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err == nil {
		t.Fatal("Connect() accepted DPIDR with the constant bit clear")
	}
	if len(target.abortWrites) != 0 || target.pendingDP != nil || target.ctrlReads != 0 || target.ctrlWrites != 0 {
		t.Fatalf("invalid DPIDR configured target: ABORT=%v pending=%v CTRL reads=%d writes=%d", target.abortWrites, target.pendingDP, target.ctrlReads, target.ctrlWrites)
	}
	target.dpidr = 0x2ba01477
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect() after valid DPIDR: %v", err)
	}
}

func TestConnectionCleansUpFailedOverrunAcquisition(t *testing.T) {
	readErr := errors.New("injected CTRL/STAT read failure")
	target := &connectionTarget{dpidr: 0x2ba01477, failCTRLAt: 2, ctrlReadErr: readErr}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); !errors.Is(err, readErr) {
		t.Fatalf("Connect() error = %v, want CTRL/STAT read failure", err)
	}
	if target.ctrlStat&testOverrunDetect != 0 {
		t.Fatalf("CTRL/STAT after failed Connect = %#08x, want ORUNDETECT restored", target.ctrlStat)
	}
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect() after automatic cleanup: %v", err)
	}
}

func TestConnectionForgetsIdentityAfterFailedInitialBootstrap(t *testing.T) {
	readErr := errors.New("injected CTRL/STAT read failure")
	target := &connectionTarget{dpidr: 0x2ba01477, failCTRLAt: 1, ctrlReadErr: readErr}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); !errors.Is(err, readErr) {
		t.Fatalf("Connect() error = %v, want CTRL/STAT read failure", err)
	}
	target.dpidr = 0x0ba01477
	target.failCTRLAt = 0
	dpidr, err := conn.Connect(t.Context())
	if err != nil {
		t.Fatalf("Connect() after failed initial bootstrap: %v", err)
	}
	if dpidr != target.dpidr {
		t.Fatalf("DPIDR = %#08x, want %#08x", dpidr, target.dpidr)
	}
}

func TestCanceledReconnectLeavesConnectionReady(t *testing.T) {
	target := &connectionTarget{dpidr: 0x2ba01477}
	wire := &countingWire{wire: sim.New(target)}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := wire.calls
	if _, err := conn.Connect(ctx); !errors.Is(err, context.Canceled) || wire.calls != before {
		t.Fatalf("canceled Connect() error = %v after %d new calls", err, wire.calls-before)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err != nil {
		t.Fatalf("ReadDP() after canceled Connect: %v", err)
	}
}

func TestConnectionRetainsCleanupAfterFailedConnectRollback(t *testing.T) {
	readErr := errors.New("injected CTRL/STAT read failure")
	entryErr := errors.New("injected cleanup entry failure")
	target := &connectionTarget{dpidr: 0x2ba01477, failCTRLAt: 2, ctrlReadErr: readErr}
	wire := &entryFailureWire{wire: sim.New(target), err: entryErr}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); !errors.Is(err, readErr) || !errors.Is(err, entryErr) {
		t.Fatalf("Connect() error = %v, want operation and cleanup failures", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded while cleanup remained pending")
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("Release() after failed rollback: %v", err)
	}
	if target.ctrlStat&testOverrunDetect != 0 {
		t.Fatalf("CTRL/STAT after repair = %#08x, want ORUNDETECT restored", target.ctrlStat)
	}
}
