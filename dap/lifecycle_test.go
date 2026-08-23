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

const (
	overrunDetect = uint32(1 << 0)
	debugRequest  = uint32(1 << 28)
	debugAck      = uint32(1 << 29)
	systemRequest = uint32(1 << 30)
	systemAck     = uint32(1 << 31)
	allPower      = debugRequest | debugAck | systemRequest | systemAck
)

type entryGuardWire struct {
	inner   swd.Wire
	entered bool
	entries int
}

type entryFailureWire struct {
	inner       swd.Wire
	entryErrors []error
	entries     int
	calls       int
}

func (w *entryFailureWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	if err != nil || bits != 136 {
		return input, err
	}
	w.entries++
	if w.entries <= len(w.entryErrors) && w.entryErrors[w.entries-1] != nil {
		return nil, w.entryErrors[w.entries-1]
	}
	return input, nil
}

func (w *entryGuardWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if bits == 136 {
		w.entered = true
		w.entries++
		return w.inner.SWDIO(ctx, direction, output, bits)
	}
	if !w.entered {
		return nil, errors.New("request sent before SWD entry")
	}
	return w.inner.SWDIO(ctx, direction, output, bits)
}

func TestConnectEntersSWDBeforeDebugPortTraffic(t *testing.T) {
	target := sim.New(0x2ba01477)
	wire := &entryGuardWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if wire.entries != 1 {
		t.Fatalf("SWD entries = %d, want 1", wire.entries)
	}
}

func TestNewDebugPortIgnoresZeroOption(t *testing.T) {
	var option dap.Option
	dp := dap.NewDebugPort(swd.New(swdsim.New(sim.New(0x2ba01477))), option)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectRepairsFailedInitialProtocolEntry(t *testing.T) {
	entryErr := errors.New("injected protocol-entry failure")
	wire := &entryFailureWire{inner: swdsim.New(sim.New(0x2ba01477)), entryErrors: []error{entryErr}}
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); !errors.Is(err, entryErr) {
		t.Fatalf("Connect() error = %v, want %v", err, entryErr)
	}
	if wire.entries != 2 {
		t.Fatalf("SWD entries after automatic repair = %d, want 2", wire.entries)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatalf("Connect() after automatic repair: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledConnectSendsNoTraffic(t *testing.T) {
	wire := &entryFailureWire{inner: swdsim.New(sim.New(0x2ba01477))}
	dp := dap.NewDebugPort(swd.New(wire))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := dp.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() error = %v, want context cancellation", err)
	}
	if wire.calls != 0 {
		t.Fatalf("canceled Connect() sent %d wire calls", wire.calls)
	}
}

func TestFailedInitialEntryRepairLeavesCleanupPending(t *testing.T) {
	entryErr := errors.New("injected protocol-entry failure")
	repairErr := errors.New("injected protocol-entry repair failure")
	wire := &entryFailureWire{inner: swdsim.New(sim.New(0x2ba01477)), entryErrors: []error{entryErr, repairErr}}
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); !errors.Is(err, entryErr) || !errors.Is(err, repairErr) {
		t.Fatalf("Connect() error = %v, want entry and repair failures", err)
	}
	before := wire.calls
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil {
		t.Fatal("ReadDP() succeeded while entry cleanup was pending")
	}
	if wire.calls != before {
		t.Fatalf("blocked ReadDP() sent %d calls", wire.calls-before)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after entry repair failure: %v", err)
	}
}

func TestConnectAndReleaseSWDP(t *testing.T) {
	target := sim.New(0x2ba01477)
	dp := newDebugPort(t, target)
	info, err := dp.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if info.Raw != 0x2ba01477 {
		t.Fatalf("DPIDR = %#08x, want 0x2ba01477", info.Raw)
	}
	assertPower(t, dp, allPower)

	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := target.Read(t.Context(), dpRead(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if state&allPower != 0 {
		t.Fatalf("power state after release = %#08x, want 0", state&allPower)
	}
	if got, ok := dp.Identity(); !ok || got != info {
		t.Fatalf("Identity() = %+v, %t; want %+v, true", got, ok, info)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("repeated Release() failed: %v", err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatalf("Connect() after release failed: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after reconnection failed: %v", err)
	}
}

func TestConnectPreservesPowerItDidNotAcquire(t *testing.T) {
	target := sim.New(0x2ba01477)
	if err := target.Write(t.Context(), dpWrite(0x04), debugRequest); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := target.Read(t.Context(), dpRead(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if state&allPower != debugRequest|debugAck {
		t.Fatalf("power state after release = %#08x, want inherited debug power", state&allPower)
	}
}

func TestConnectRollsBackAfterAcknowledgementTimeout(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477, suppressAck: true}
	dp := newDebugPort(t, target)
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if _, err := dp.Connect(ctx); err == nil {
		t.Fatal("Connect() succeeded without power acknowledgements")
	}
	if target.ctrl&allPower != 0 {
		t.Fatalf("power state after rollback = %#08x, want 0", target.ctrl)
	}
	target.suppressAck = false
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatalf("Connect() after automatic rollback failed: %v", err)
	}
}

func TestConnectRejectsSecondConnection(t *testing.T) {
	dp := newDebugPort(t, sim.New(0x2ba01477))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err == nil {
		t.Fatal("second Connect() succeeded")
	}
}

func TestReleaseCanRetryAfterWriteFailure(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477, failRelease: true}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err == nil {
		t.Fatal("Release() succeeded despite the injected write failure")
	}
	if err := dp.SetMaxWaits(1); err == nil {
		t.Fatal("SetMaxWaits() succeeded while release was pending")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("retried Release() failed: %v", err)
	}
	if err := dp.SetMaxWaits(1); err != nil {
		t.Fatalf("SetMaxWaits() after retried Release: %v", err)
	}
	if target.ctrl&allPower != 0 {
		t.Fatalf("power state after retry = %#08x, want 0", target.ctrl)
	}
}

func TestConnectClearsStickyStateBeforeSelecting(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477, sticky: true, ackAfter: 2}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(target.abortValues) == 0 || target.abortValues[0] != 0x1e {
		t.Fatalf("ABORT writes = %#v, want full-DP sticky clear 0x1e", target.abortValues)
	}
	if target.ackReads < 2 {
		t.Fatalf("power acknowledgement was not polled: %d reads", target.ackReads)
	}
}

func TestConnectDoesNotClearUnsupportedMinimalDPState(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba11477, sticky: true}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(target.abortValues) == 0 || target.abortValues[0] != 0x1c {
		t.Fatalf("ABORT writes = %#v, want minimal-DP sticky clear 0x1c", target.abortValues)
	}
}

func TestConnectReadsIdentityBeforeConfiguration(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectPreservesInheritedOverrunDetection(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.SetOverrunDetect(true)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("Release() cleared inherited ORUNDETECT")
	}
}

func TestConnectAcquiresAndRestoresOverrunDetection(t *testing.T) {
	target := sim.New(0x2ba01477)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("Connect() left ORUNDETECT clear")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.OverrunDetectEnabled() {
		t.Fatal("Release() left acquired ORUNDETECT set")
	}
}

func TestDebugPortRejectsOverrunChangesBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, state&^overrunDetect); err == nil {
		t.Fatal("WriteDP(CTRLSTAT) changed connection-owned ORUNDETECT")
	}
	txn := dp.NewTxn()
	write := txn.WriteDP(dap.CTRLSTAT, state&^overrunDetect)
	if err := txn.Commit(t.Context()); err == nil || write.Err() == nil {
		t.Fatal("Txn.WriteDP(CTRLSTAT) changed connection-owned ORUNDETECT")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("rejected ORUNDETECT writes sent %d requests", got-before)
	}
}

func newDebugPort(t *testing.T, target swdsim.Target) *dap.DebugPort {
	t.Helper()
	conn := swd.New(swdsim.New(target))
	return dap.NewDebugPort(conn)
}

func assertPower(t *testing.T, dp *dap.DebugPort, want uint32) {
	t.Helper()
	got, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	if got&allPower != want {
		t.Fatalf("power state = %#08x, want %#08x", got&allPower, want)
	}
}

type powerTarget struct {
	dpidr        uint32
	ctrl         uint32
	identified   bool
	sticky       bool
	suppressAck  bool
	ackAfter     int
	ackReads     int
	failRelease  bool
	abortValues  []uint32
	selectValues []uint32
	dpBank       uint32
}

func (t *powerTarget) Acknowledge(_ context.Context, req swdsim.Request) error {
	if !req.AP && !req.Read && req.Addr == 0x08 && t.sticky {
		return swd.ErrFault
	}
	return nil
}

func (t *powerTarget) Read(_ context.Context, req swdsim.Request) (uint32, error) {
	if req.AP {
		return 0, errors.New("unexpected AP read")
	}
	switch req.Addr {
	case 0:
		t.identified = true
		return t.dpidr, nil
	case 4:
		t.ackReads++
		if t.dpBank != 0 {
			return 0, nil
		}
		value := t.ctrl
		if t.suppressAck || t.ackReads < t.ackAfter {
			value &^= debugAck | systemAck
		}
		return value, nil
	case 0x0c:
		return 0, nil
	default:
		return 0, errors.New("unexpected DP read")
	}
}

func (t *powerTarget) Write(_ context.Context, req swdsim.Request, value uint32) error {
	if req.AP {
		return errors.New("unexpected AP write")
	}
	if !t.identified {
		return errors.New("DP configuration written before DPIDR was read")
	}
	switch req.Addr {
	case 0:
		t.abortValues = append(t.abortValues, value)
		if value&0x1c == 0x1c {
			t.sticky = false
		}
	case 4:
		if value&(debugRequest|systemRequest) == 0 &&
			t.ctrl&(debugRequest|systemRequest) != 0 && t.failRelease {
			t.failRelease = false
			return errors.New("injected release failure")
		}
		t.ctrl = value & (debugRequest | systemRequest)
		if t.ctrl&debugRequest != 0 {
			t.ctrl |= debugAck
		}
		if t.ctrl&systemRequest != 0 {
			t.ctrl |= systemAck
		}
	case 8:
		t.selectValues = append(t.selectValues, value)
		t.dpBank = value & 0x0f
	default:
		return errors.New("unexpected DP write")
	}
	return nil
}
