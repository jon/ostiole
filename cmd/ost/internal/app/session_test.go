package app

import (
	"context"
	"errors"
	"github.com/jon/ostiole/swd"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/probe"
	swdsim "github.com/jon/ostiole/swd/sim"
)

var errSessionRestore = errors.New("session restoration unavailable")

type sessionTarget struct {
	*dapsim.Target
	failWrites bool
	afterRead  func(context.Context, swdsim.Request, uint32)
}

func (p *sessionTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := p.Target.Read(ctx, req)
	if err == nil && p.afterRead != nil {
		p.afterRead(ctx, req, value)
	}
	return value, err
}

func (p *sessionTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if p.failWrites && req.AP {
		return errSessionRestore
	}
	return p.Target.Write(ctx, req, value)
}

type sessionHost struct {
	activations int
	*swdsim.Wire
	closes, transfers                int
	possible, pending                bool
	retainHost                       bool
	closeErr, wireErr, activationErr error
	beforeClose                      func()
}

func (h *sessionHost) SWD(context.Context, probe.SWDConfig) (probe.Wire, error) {
	h.activations++
	return h, h.activationErr
}

func (h *sessionHost) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	h.transfers++
	if h.wireErr != nil {
		return nil, h.wireErr
	}
	return h.Wire.SWDIO(ctx, direction, output, bits)
}

func (h *sessionHost) open(context.Context) (*probe.Probe, error) {
	return probe.New(probe.Info{Serial: "session fixture"}, h), nil
}

func (h *sessionHost) RestorationPossible() bool { return h.possible }
func (h *sessionHost) CleanupPending() bool      { return h.pending }

func (h *sessionHost) Close() error {
	h.closes++
	h.pending = h.retainHost
	if h.beforeClose != nil {
		h.beforeClose()
	}
	return h.closeErr
}

func newSessionBench(t *testing.T) (*sessionTarget, *sessionHost) {
	t.Helper()
	peer := &sessionTarget{Target: dapsim.New(0x2ba01477)}
	if err := peer.AddMEMAP(dap.NewAPSel(0), 0x24770011, map[uint32]uint32{0xe000ed00: 0x410cc200}); err != nil {
		t.Fatal(err)
	}
	host := &sessionHost{Wire: swdsim.New(peer), possible: true, pending: true}
	return peer, host
}

func openSessionFixture(t *testing.T) (*dapSession, *sessionTarget, *sessionHost) {
	t.Helper()
	peer, host := newSessionBench(t)
	session, err := openDAPWith(t.Context(), host.open)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := session.owner.OpenMemAP(t.Context(), dap.NewAPSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	session.memory = memory
	return session, peer, host
}

func TestSessionReleaseFailureRetainsLowerOwners(t *testing.T) {
	session, peer, host := openSessionFixture(t)
	peer.failWrites = true
	if err := session.close(); !errors.Is(err, errSessionRestore) || host.closes != 0 {
		t.Fatalf("release=%v host closes=%d", err, host.closes)
	}
	peer.failWrites = false
	if err := session.close(); err != nil || host.closes != 1 {
		t.Fatalf("retry release=%v host closes=%d", err, host.closes)
	}
}

func TestSessionOperationsUseRealDAPAndCleanup(t *testing.T) {
	for _, operation := range []string{"swd", "dp", "ap", "target"} {
		t.Run(operation, func(t *testing.T) {
			peer, host := newSessionBench(t)
			host.beforeClose = func() {
				if peer.OverrunDetectEnabled() {
					t.Error("host closed before SWD restoration")
				}
			}
			var err error
			switch operation {
			case "swd":
				var value uint32
				value, err = readDPIDRWith(t.Context(), host.open)
				if value != 0x2ba01477 {
					t.Errorf("DPIDR=%#x", value)
				}
			case "dp":
				var info dap.DPIDRInfo
				info, err = inspectDPWith(t.Context(), host.open)
				if info.Raw != 0x2ba01477 {
					t.Errorf("DPIDR=%#x", info.Raw)
				}
			case "ap":
				var info apIdentity
				info, err = inspectAPWith(t.Context(), dap.NewAPSel(0), host.open)
				if info.idr.Raw != 0x24770011 {
					t.Errorf("APIDR=%#x", info.idr.Raw)
				}
			case "target":
				var info targetIdentity
				info, err = identifyCortexMWith(t.Context(), dap.NewAPSel(0), host.open)
				if info.processor.Raw != 0x410cc200 {
					t.Errorf("CPUID=%#x", info.processor.Raw)
				}
			}
			if err != nil || host.closes != 1 || host.transfers == 0 {
				t.Fatalf("operation=%v host closes=%d transfers=%d", err, host.closes, host.transfers)
			}
		})
	}
}

func TestTerminalSessionCloseReleasesHostWithoutProtocolTraffic(t *testing.T) {
	for _, raw := range []bool{false, true} {
		closeSession, host := sessionCloserFixture(t, raw)
		host.possible = false
		host.wireErr = errors.New("terminal attachment loss")
		host.closeErr = errors.New("final host close failure")
		calls := host.transfers
		err := closeSession()
		if !errors.Is(err, armdebug.ErrRestorationAbandoned) || !errors.Is(err, host.closeErr) || host.closes != 1 || host.transfers != calls {
			t.Fatalf("terminal close=%v host closes=%d transfers=%d/%d", err, host.closes, host.transfers, calls)
		}
		if err := closeSession(); err != nil || host.closes != 1 || host.transfers != calls {
			t.Fatalf("repeated terminal close=%v", err)
		}
	}
}

func sessionCloserFixture(t *testing.T, raw bool) (func() error, *sessionHost) {
	t.Helper()
	if raw {
		_, host := newSessionBench(t)
		session, err := openSWDWith(t.Context(), host.open)
		if err != nil {
			t.Fatal(err)
		}
		return session.close, host
	}
	session, _, host := openSessionFixture(t)
	return session.close, host
}

type canceledSessionRAM struct {
	*dapsim.RAM
	cancel context.CancelFunc
	reads  int
}

func (m *canceledSessionRAM) Read(ctx context.Context, address uint64, data []byte) error {
	m.reads++
	if err := m.RAM.Read(ctx, address, data); err != nil {
		return err
	}
	m.cancel()
	return nil
}

func TestCanceledTargetOperationJoinsFreshCleanupError(t *testing.T) {
	peer, host := newSessionBench(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	device := &canceledSessionRAM{RAM: &dapsim.RAM{}, cancel: cancel}
	if err := device.SetBytes(0xe000ed00, []byte{0, 0xc2, 0x0c, 0x41}); err != nil {
		t.Fatal(err)
	}
	if err := peer.MapMEMAPDevice(dap.NewAPSel(0), 0xe000ed00, 4, device); err != nil {
		t.Fatal(err)
	}
	host.closeErr = errors.New("final host cleanup report")
	host.beforeClose = func() {
		if peer.OverrunDetectEnabled() {
			t.Error("cleanup did not restore state after operation cancellation")
		}
	}
	_, err := identifyCortexMWith(ctx, dap.NewAPSel(0), host.open)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, host.closeErr) || host.closes != 1 || device.reads != 1 {
		t.Fatalf("operation/cleanup=%v host closes=%d target reads=%d", err, host.closes, device.reads)
	}
}

func TestPartialOpenerFailureClosesRetainedProbeWithoutActivation(t *testing.T) {
	for _, raw := range []bool{false, true} {
		_, host := newSessionBench(t)
		cause := errors.New("probe open failure with retained cleanup")
		host.closeErr = errors.New("final opening cleanup report")
		open := func(ctx context.Context) (*probe.Probe, error) {
			opened, _ := host.open(ctx)
			return opened, cause
		}
		var err error
		if raw {
			_, err = readDPIDRWith(t.Context(), open)
		} else {
			_, err = inspectDPWith(t.Context(), open)
		}
		if !errors.Is(err, cause) || !errors.Is(err, host.closeErr) || host.closes != 1 || host.transfers != 0 || host.activations != 0 {
			t.Fatalf("partial open=%v host=%+v", err, host)
		}
	}
}

func TestRawSessionReleaseFailureRetainsProbeForRetry(t *testing.T) {
	peer, host := newSessionBench(t)
	session, err := openSWDWith(t.Context(), host.open)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("release transport unavailable")
	host.wireErr = cause
	if err := session.close(); !errors.Is(err, cause) || host.closes != 0 {
		t.Fatalf("live release=%v host closes=%d", err, host.closes)
	}
	host.wireErr = nil
	host.beforeClose = func() {
		if peer.OverrunDetectEnabled() {
			t.Error("host closed before restored response mode")
		}
	}
	if err := session.close(); err != nil || host.closes != 1 {
		t.Fatalf("release retry=%v host closes=%d", err, host.closes)
	}
	if err := session.close(); err != nil || host.closes != 1 {
		t.Fatalf("repeated release=%v", err)
	}
}

func TestSessionTerminalHostRetryReportsFinalErrorsOnce(t *testing.T) {
	for _, raw := range []bool{false, true} {
		closeSession, host := sessionCloserFixture(t, raw)
		host.possible, host.retainHost = false, true
		host.closeErr = errors.New("pending host close")
		calls := host.transfers
		if err := closeSession(); !errors.Is(err, armdebug.ErrRestorationAbandoned) || !errors.Is(err, host.closeErr) || host.closes != 1 {
			t.Fatalf("first terminal cleanup=%v", err)
		}
		host.retainHost = false
		if err := closeSession(); !errors.Is(err, host.closeErr) || errors.Is(err, armdebug.ErrRestorationAbandoned) || host.closes != 2 {
			t.Fatalf("final host cleanup=%v closes=%d", err, host.closes)
		}
		if err := closeSession(); err != nil || host.closes != 2 || host.transfers != calls {
			t.Fatalf("completed host cleanup=%v", err)
		}
	}
}

func TestSessionActivationFailureRetainsBothErrors(t *testing.T) {
	for _, raw := range []bool{false, true} {
		_, host := newSessionBench(t)
		host.activationErr = errors.New("activation failed")
		host.closeErr = errors.New("activation cleanup failed finally")
		var err error
		if raw {
			_, err = readDPIDRWith(t.Context(), host.open)
		} else {
			_, err = inspectDPWith(t.Context(), host.open)
		}
		if !errors.Is(err, host.activationErr) || !errors.Is(err, host.closeErr) || host.closes != 1 || host.transfers != 0 {
			t.Fatalf("activation/cleanup=%v closes=%d transfers=%d", err, host.closes, host.transfers)
		}
	}
}

func TestRawSessionCompletedReleaseErrorStillClosesHost(t *testing.T) {
	peer, host := newSessionBench(t)
	connection := swd.New(host, swd.WithCompletionTimeout(3*time.Second))
	if _, err := connection.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	session := &swdSession{opened: probe.New(probe.Info{}, host), connection: connection}
	peer.afterRead = func(ctx context.Context, req swdsim.Request, value uint32) {
		if !req.AP && req.Read && req.Addr == 4 && value&1 == 0 {
			// Let the caller's one-second cleanup deadline expire while this final
			// frame still has its independent three-second completion budget.
			timer := time.NewTimer(1100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Error("completion budget expired before final response")
			}
		}
	}
	err := session.close()
	if !errors.Is(err, context.DeadlineExceeded) || connection.CleanupPending() || host.closes != 1 {
		t.Fatalf("completed release=%v pending=%v host closes=%d", err, connection.CleanupPending(), host.closes)
	}
	calls := host.transfers
	if err := session.close(); err != nil || host.closes != 1 || host.transfers != calls {
		t.Fatalf("completed close repeated traffic: %v", err)
	}
}
