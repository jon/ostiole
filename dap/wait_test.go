package dap_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

const testWriteDataError = uint32(1 << 7)

func dpRead(addr uint8) swdsim.Request {
	return swdsim.Request{Read: true, Addr: addr}
}

func dpWrite(addr uint8) swdsim.Request {
	return swdsim.Request{Addr: addr}
}

func apRead(addr uint8) swdsim.Request {
	return swdsim.Request{AP: true, Read: true, Addr: addr}
}

func apWrite(addr uint8) swdsim.Request {
	return swdsim.Request{AP: true, Addr: addr}
}

type waitTarget struct {
	*dapsim.Target
	waitFor           swdsim.Request
	waits             int
	waitSkip          int
	fault             bool
	faultAfter        int
	armed             bool
	accepted          bool
	attempts          int
	executed          map[swdsim.Request]int
	abortValues       []uint32
	abortErr          error
	waitAfterAbort    bool
	clearErr          error
	stickyAfterAbort  uint32
	sticky            uint32
	retryErr          error
	aborted           bool
	selectWaits       int
	selectAttempts    int
	writeErrFor       swdsim.Request
	writeErr          error
	writeErrSkip      int
	dropWriteFor      swdsim.Request
	dropWrite         bool
	stickyOnDrop      uint32
	deferWriteFor     swdsim.Request
	deferWrite        bool
	bufferedWrite     bool
	bufferedValue     uint32
	dropStickyClear   bool
	ctrlStatErr       error
	readErrAfterAbort error
	requests          []swdsim.Request
	dpidrOverride     uint32
	selectValues      []uint32
}

type cleanupFailWire struct {
	inner                swd.Wire
	err                  error
	failBits             int
	cancel               context.CancelFunc
	armed                bool
	lost                 bool
	reentries            int
	trafficBeforeReentry int
	onReentry            func()
}

type reentryFailWire struct {
	inner           swd.Wire
	reentryErr      error
	entries         int
	failEntries     int
	reentryAttempts int
}

func (w *reentryFailWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if bits == 136 {
		w.entries++
		if w.entries > 1 {
			w.reentryAttempts++
			if w.failEntries > 0 {
				w.failEntries--
				return nil, w.reentryErr
			}
		}
	}
	return w.inner.SWDIO(ctx, direction, output, bits)
}

func (w *cleanupFailWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	if w.lost {
		if err == nil && bits == 136 {
			w.lost = false
			w.reentries++
			if w.onReentry != nil {
				w.onReentry()
			}
		} else {
			w.trafficBeforeReentry++
		}
		return input, err
	}
	fail := bits == w.failBits
	if w.failBits == 0 {
		fail = bits == 54 && len(output) != 0 && output[0] == 0x81
	}
	if err == nil && w.armed && fail {
		w.armed = false
		w.lost = true
		if w.cancel != nil {
			w.cancel()
			return input, nil
		}
		return nil, w.err
	}
	return input, err
}

func newWaitTarget() *waitTarget {
	return &waitTarget{
		Target:   dapsim.New(0x2ba01477),
		executed: make(map[swdsim.Request]int),
	}
}

func (t *waitTarget) arm(req swdsim.Request, waits int) {
	t.waitFor = req
	t.waits = waits
	t.fault = false
	t.faultAfter = 0
	t.armed = true
	t.accepted = false
	t.attempts = 0
	t.executed[req] = 0
}

func (t *waitTarget) armFault(req swdsim.Request) {
	t.arm(req, 0)
	t.fault = true
}

func (t *waitTarget) armFaultAfter(req swdsim.Request, successful int) {
	t.armFault(req)
	t.faultAfter = successful
}

func (t *waitTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	t.requests = append(t.requests, req)
	if err := t.acknowledgeTarget(ctx, req); err != nil {
		return err
	}
	if t.aborted && t.waitAfterAbort && req == (dpRead(0x0c)) {
		return swd.ErrWait
	}
	if t.aborted && req == (dpWrite(0x08)) && t.selectWaits != 0 {
		t.selectAttempts++
		if t.selectWaits > 0 {
			t.selectWaits--
		}
		return swd.ErrWait
	}
	if !t.armed || req != t.waitFor {
		return nil
	}
	if t.waitSkip > 0 {
		t.waitSkip--
		return nil
	}
	return t.acknowledgeArmedRequest()
}

func (t *waitTarget) acknowledgeArmedRequest() error {
	t.attempts++
	if t.fault && t.faultAfter > 0 {
		t.faultAfter--
		return nil
	}
	if t.retryErr != nil && t.attempts == 2 {
		return t.retryErr
	}
	if t.waits != 0 {
		if t.waits > 0 {
			t.waits--
		}
		return swd.ErrWait
	}
	if t.fault {
		t.armed = false
		return swd.ErrFault
	}
	t.accepted = true
	return nil
}

func (t *waitTarget) acknowledgeTarget(ctx context.Context, req swdsim.Request) error {
	if t.sticky != 0 && !stickyExempt(req) {
		return swd.ErrFault
	}
	return t.Target.Acknowledge(ctx, req)
}

func stickyExempt(req swdsim.Request) bool {
	if req.AP {
		return false
	}
	return req.Read && (req.Addr == 0x00 || req.Addr == 0x04) || !req.Read && req.Addr == 0x00
}

func (t *waitTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	t.executed[req]++
	t.finishAccepted(req)
	if err := t.flushBufferedWrite(ctx, req); err != nil {
		return 0, err
	}
	if !req.AP && req.Read && req.Addr == 0x04 && t.ctrlStatErr != nil {
		err := t.ctrlStatErr
		t.ctrlStatErr = nil
		return 0, err
	}
	if !req.AP && req.Read && req.Addr == 0x00 && t.dpidrOverride != 0 {
		return t.dpidrOverride, nil
	}
	if t.aborted && !req.AP && req.Read && req.Addr == 0x04 && t.readErrAfterAbort != nil {
		err := t.readErrAfterAbort
		t.readErrAfterAbort = nil
		return 0, err
	}
	value, err := t.Target.Read(ctx, req)
	if !req.AP && req.Addr == 0x04 {
		value |= t.sticky
	}
	return value, err
}

func (t *waitTarget) flushBufferedWrite(ctx context.Context, req swdsim.Request) error {
	if !t.bufferedWrite || req != (dpRead(0x0c)) {
		return nil
	}
	t.bufferedWrite = false
	return t.Target.Write(ctx, t.deferWriteFor, t.bufferedValue)
}

func (t *waitTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	t.executed[req]++
	t.finishAccepted(req)
	if t.dropWrite && req == t.dropWriteFor {
		t.dropWrite = false
		t.sticky |= t.stickyOnDrop
		return nil
	}
	if t.deferWrite && req == t.deferWriteFor {
		t.deferWrite = false
		t.bufferedWrite = true
		t.bufferedValue = value
		return nil
	}
	if !req.AP && req.Addr == 0x00 {
		if err := t.writeAbort(value); err != nil {
			return err
		}
	}
	if !req.AP && req.Addr == 0x08 {
		t.selectValues = append(t.selectValues, value)
	}
	if err := t.Target.Write(ctx, req, value); err != nil {
		return err
	}
	if req == t.writeErrFor && t.writeErr != nil && t.writeErrSkip > 0 {
		t.writeErrSkip--
	} else if req == t.writeErrFor && t.writeErr != nil {
		err := t.writeErr
		t.writeErr = nil
		return err
	}
	return nil
}

func (t *waitTarget) writeAbort(value uint32) error {
	t.abortValues = append(t.abortValues, value)
	if t.bufferedWrite {
		t.bufferedWrite = false
		t.sticky |= testWriteDataError
	}
	if value&1 != 0 {
		t.armed = false
		t.aborted = true
		t.sticky |= t.stickyAfterAbort
		if t.abortErr != nil {
			return t.abortErr
		}
	}
	if value&0x1e != 0 && t.clearErr != nil {
		return t.clearErr
	}
	if value&0x1e != 0 && t.dropStickyClear {
		t.dropStickyClear = false
		return nil
	}
	if value&(1<<1) != 0 {
		t.sticky &^= 1 << 4
	}
	if value&(1<<2) != 0 {
		t.sticky &^= 1 << 5
	}
	if value&(1<<3) != 0 {
		t.sticky &^= 1 << 7
	}
	if value&(1<<4) != 0 {
		t.sticky &^= 1 << 1
	}
	return nil
}

func (t *waitTarget) finishAccepted(req swdsim.Request) {
	if t.armed && t.accepted && req == t.waitFor {
		t.armed = false
		t.accepted = false
		t.fault = false
	}
}

func TestDebugPortRetriesOnlyTheWAITedTransfer(t *testing.T) {
	selectReq := dpWrite(0x08)
	apReadReq := apRead(0x0c)
	apWriteReq := apWrite(0x00)
	rdbuffReq := dpRead(0x0c)
	tests := []struct {
		name       string
		waitFor    swdsim.Request
		logicalReq swdsim.Request
		skip       int
		operation  func(*testing.T, *dap.DebugPort)
	}{
		{name: "SELECT before AP read", waitFor: selectReq, logicalReq: apReadReq, operation: readAPIDR},
		{name: "AP read request", waitFor: apReadReq, logicalReq: apReadReq, operation: readAPIDR},
		{name: "RDBUFF after AP read", waitFor: rdbuffReq, logicalReq: apReadReq, skip: 1, operation: readAPIDR},
		{name: "AP write request", waitFor: apWriteReq, logicalReq: apWriteReq, operation: writeAPCSW},
		{name: "RDBUFF after AP write", waitFor: rdbuffReq, logicalReq: apWriteReq, operation: writeAPCSW},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newWaitTarget()
			addAP(t, target, 0, 0x24770011)
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := dp.Release(context.Background()); err != nil {
					t.Errorf("release SW-DP: %v", err)
				}
			})

			target.arm(test.waitFor, 2)
			target.waitSkip = test.skip
			test.operation(t, dp)
			assertWAITReplay(t, target, test.waitFor, test.logicalReq, test.skip)
		})
	}
}

func TestDebugPortDoesNotDelayWAITRetries(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	req := apRead(0x0c)
	target.arm(req, 100)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	value, err := dp.ReadAPIDR(ctx, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if value.Raw != 0x24770011 {
		t.Fatalf("AP IDR = %#08x", value.Raw)
	}
}

func TestDebugPortClearsStickyOverrunBeforeRetry(t *testing.T) {
	target := newWaitTarget()
	target.SetOverrunDetect(true)
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	req := apRead(0x0c)
	target.arm(req, 2)
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatal(err)
	}
	clears := 0
	for _, value := range target.abortValues {
		if value == 1<<4 {
			clears++
		}
	}
	if clears != 2 {
		t.Fatalf("STICKYORUN clears = %d, want 2; ABORT writes = %#v", clears, target.abortValues)
	}
}

func TestDebugPortRecoversFAULTInOverrunMode(t *testing.T) {
	target := newWaitTarget()
	target.SetOverrunDetect(true)
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.armFault(apRead(0x0c))
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !fault.StateValid || fault.CTRLSTAT&1<<1 == 0 {
		t.Fatalf("ReadAPIDR() error = %v, want STICKYORUN FAULT", err)
	}
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatalf("ReadAPIDR() after FAULT cleanup: %v", err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("FAULT cleanup cleared ORUNDETECT")
	}
}

func TestInvalidDPRegisterDoesNotLoseFraming(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *dap.DebugPort) error
	}{
		{
			name: "read",
			run: func(ctx context.Context, dp *dap.DebugPort) error {
				_, err := dp.ReadDP(ctx, dap.DPRegister(0x10))
				return err
			},
		},
		{
			name: "write",
			run: func(ctx context.Context, dp *dap.DebugPort) error {
				return dp.WriteDP(ctx, dap.DPRegister(0x10), 0)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newWaitTarget()
			addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0: 1})
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
			if err != nil {
				t.Fatal(err)
			}

			before := len(target.requests)
			if err := test.run(t.Context(), dp); err == nil || !strings.Contains(err.Error(), "invalid DP register") {
				t.Fatalf("invalid DP operation error = %v", err)
			}
			if got := len(target.requests); got != before {
				t.Fatalf("requests after invalid DP operation = %d, want %d", got, before)
			}
			if _, err := mem.ReadWord(t.Context(), 0); err != nil {
				t.Fatalf("ReadWord() after invalid DP operation: %v", err)
			}
			if err := mem.Release(t.Context()); err != nil {
				t.Fatalf("MemAP.Release(): %v", err)
			}
			if err := dp.Release(t.Context()); err != nil {
				t.Fatalf("DebugPort.Release(): %v", err)
			}
		})
	}
}

func TestConnectRepairsRejectedBootstrapByReenteringSWD(t *testing.T) {
	tests := []struct {
		name         string
		arm          func(*waitTarget, swdsim.Request)
		want         error
		wantAttempts int
	}{
		{
			name:         "WAIT",
			arm:          func(target *waitTarget, req swdsim.Request) { target.arm(req, 1) },
			want:         swd.ErrWait,
			wantAttempts: 2,
		},
		{
			name:         "FAULT",
			arm:          func(target *waitTarget, req swdsim.Request) { target.armFault(req) },
			want:         swd.ErrFault,
			wantAttempts: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newWaitTarget()
			wire := &reentryFailWire{inner: swdsim.New(target)}
			conn := swd.New(wire)
			dp := dap.NewDebugPort(conn)
			req := dpWrite(0x08)
			test.arm(target, req)

			_, err := dp.Connect(t.Context())
			if !errors.Is(err, test.want) {
				t.Fatalf("Connect() error = %v, want %v", err, test.want)
			}
			if target.attempts != test.wantAttempts || target.executed[req] != 1 {
				t.Fatalf("bank-zero SELECT attempted %d times and executed %d times during repair", target.attempts, target.executed[req])
			}
			if wire.reentryAttempts != 1 {
				t.Fatalf("SWD re-entry attempts = %d, want 1", wire.reentryAttempts)
			}
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatalf("Connect() after automatic repair: %v", err)
			}
			if err := dp.Release(t.Context()); err != nil {
				t.Fatalf("Release() after repaired connection: %v", err)
			}
		})
	}
}

func TestConnectCanRetryAfterBootstrapRepair(t *testing.T) {
	tests := []struct {
		name string
		arm  func(*waitTarget, swdsim.Request)
		want error
	}{
		{
			name: "WAIT",
			arm:  func(target *waitTarget, req swdsim.Request) { target.arm(req, 1) },
			want: swd.ErrWait,
		},
		{
			name: "FAULT",
			arm:  func(target *waitTarget, req swdsim.Request) { target.armFault(req) },
			want: swd.ErrFault,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newWaitTarget()
			dp := newDebugPort(t, target)
			req := dpWrite(0x08)
			test.arm(target, req)

			if _, err := dp.Connect(t.Context()); !errors.Is(err, test.want) {
				t.Fatalf("Connect() error = %v, want %v", err, test.want)
			}
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatalf("Connect() after recovery: %v", err)
			}
			if err := dp.Release(t.Context()); err != nil {
				t.Fatalf("Release() after recovered connection: %v", err)
			}
		})
	}
}

func TestFailedConnectRepairBlocksOperations(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	repairErr := errors.New("injected protocol re-entry failure")
	wire := &reentryFailWire{inner: swdsim.New(target), reentryErr: repairErr}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.armFault(dpWrite(0x08))
	wire.failEntries = 1
	_, err := dp.Connect(t.Context())
	if !errors.Is(err, swd.ErrFault) || !errors.Is(err, repairErr) {
		t.Fatalf("Connect() error = %v, want FAULT and repair failure", err)
	}
	before := len(target.requests)
	assertRepairBlocksTraffic(t, dp, target, before)
	if info, ok := dp.Identity(); !ok || info.Raw != 0x2ba01477 {
		t.Fatalf("Identity() = %+v, %t after repair failure", info, ok)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after failed connection repair: %v", err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatalf("Connect() after explicit repair: %v", err)
	}
}

func assertRepairBlocksTraffic(t *testing.T, dp *dap.DebugPort, target *waitTarget, before int) {
	t.Helper()
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil || !strings.Contains(err.Error(), "cleanup is pending") {
		t.Fatalf("ReadDP() error while cleanup pending = %v", err)
	}
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err == nil || !strings.Contains(err.Error(), "cleanup is pending") {
		t.Fatalf("ReadAPIDR() error while cleanup pending = %v", err)
	}
	if _, err := dap.OpenMemAP(t.Context(), dp, apSel(0)); err == nil || !strings.Contains(err.Error(), "cleanup is pending") {
		t.Fatalf("OpenMemAP() error while cleanup pending = %v", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after blocked operations = %d, want %d", got, before)
	}
}

func readAPIDR(t *testing.T, dp *dap.DebugPort) {
	t.Helper()
	value, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if value.Raw != 0x24770011 {
		t.Fatalf("AP IDR = %#08x", value.Raw)
	}
}

func writeAPCSW(t *testing.T, dp *dap.DebugPort) {
	t.Helper()
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 0x23000040); err != nil {
		t.Fatal(err)
	}
}

func assertWAITReplay(t *testing.T, target *waitTarget, waitFor, logicalReq swdsim.Request, priorExecutions int) {
	t.Helper()
	if target.attempts != 3 {
		t.Fatalf("WAITed transfer attempts = %d, want 3", target.attempts)
	}
	if target.executed[waitFor] != priorExecutions+1 {
		t.Fatalf("WAITed transfer executions = %d, want %d", target.executed[waitFor], priorExecutions+1)
	}
	if target.executed[logicalReq] != 1 {
		t.Fatalf("logical AP request executions = %d, want 1", target.executed[logicalReq])
	}
}

func TestDebugPortDoesNotRetryRequestsWhichMustNotWAIT(t *testing.T) {
	tests := []struct {
		name  string
		req   swdsim.Request
		apply func(context.Context, *dap.DebugPort) error
	}{
		{
			name: "DPIDR read",
			req:  dpRead(0x00),
			apply: func(ctx context.Context, dp *dap.DebugPort) error {
				_, err := dp.ReadDP(ctx, dap.DPIDR)
				return err
			},
		},
		{
			name: "CTRLSTAT read",
			req:  dpRead(0x04),
			apply: func(ctx context.Context, dp *dap.DebugPort) error {
				_, err := dp.ReadDP(ctx, dap.CTRLSTAT)
				return err
			},
		},
		{
			name: "ABORT write",
			req:  dpWrite(0x00),
			apply: func(ctx context.Context, dp *dap.DebugPort) error {
				return dp.WriteDP(ctx, dap.ABORT, 0x1e)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newWaitTarget()
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			target.arm(test.req, 1)
			err := test.apply(t.Context(), dp)
			if !errors.Is(err, swd.ErrWait) {
				t.Fatalf("operation error = %v, want %v", err, swd.ErrWait)
			}
			if target.attempts != 1 || target.executed[test.req] != 0 {
				t.Fatalf("request attempted %d times and executed %d times", target.attempts, target.executed[test.req])
			}
		})
	}
}

func TestDebugPortRetriesBankedDPRegisterWAIT(t *testing.T) {
	target := newWaitTarget()
	target.dpidrOverride = 0x2ba02477
	if err := target.SetDPRegister(dap.TARGETID, 0x12345678); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0x120000f2); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); err != nil {
		t.Fatal(err)
	}

	req := dpRead(0x04)
	target.arm(req, 2)
	if _, err := dp.ReadDP(t.Context(), dap.TARGETID); err != nil {
		t.Fatal(err)
	}
	if target.attempts != 3 || target.executed[req] != 1 {
		t.Fatalf("banked DP read attempted %d times and executed %d times", target.attempts, target.executed[req])
	}
}

func TestDebugPortReusesFullSELECTValue(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 2, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	selectReq := dpWrite(0x08)
	target.executed[selectReq] = 0
	for range 2 {
		if _, err := dp.ReadAPIDR(t.Context(), apSel(2)); err != nil {
			t.Fatal(err)
		}
	}
	if got := target.executed[selectReq]; got != 1 {
		t.Fatalf("SELECT writes = %d, want 1", got)
	}
}

func TestDebugPortAbortsExtendedWAITAndClearsAbandonedWrite(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	apWriteReq := apWrite(0x00)
	rdbuffReq := dpRead(0x0c)
	target.stickyAfterAbort = testWriteDataError
	target.arm(rdbuffReq, -1)
	err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 0x23000040)
	if !errors.Is(err, swd.ErrWait) {
		t.Fatalf("WriteRawAP() error = %v, want %v", err, swd.ErrWait)
	}
	if target.executed[apWriteReq] != 1 {
		t.Fatalf("AP write executions = %d, want 1", target.executed[apWriteReq])
	}
	if len(target.abortValues) < 2 || target.abortValues[len(target.abortValues)-2] != 1 ||
		target.abortValues[len(target.abortValues)-1] != 1<<3 {
		t.Fatalf("ABORT writes = %#v, want DAPABORT followed by WDERRCLR", target.abortValues)
	}
	state, readErr := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state&testWriteDataError != 0 {
		t.Fatalf("CTRL/STAT = %#08x after WAIT recovery", state)
	}
}

func TestDebugPortDoesNotAbortExtendedDPWAIT(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.arm(dpWrite(0x08), -1)
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrWait) {
		t.Fatalf("ReadAPIDR() error = %v, want %v", err, swd.ErrWait)
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, DAPABORT is reserved for an AP transaction", target.abortValues)
		}
	}
}

func TestDebugPortDoesNotRetryFAULT(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	req := apRead(0x0c)
	target.armFault(req)
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrFault) {
		t.Fatalf("ReadAPIDR() error = %v, want %v", err, swd.ErrFault)
	}
	if target.attempts != 1 || target.executed[req] != 0 {
		t.Fatalf("FAULTed request attempted %d times and executed %d times", target.attempts, target.executed[req])
	}
}

func TestDebugPortReportsAndClearsFAULT(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.sticky = 1<<4 | 1<<5 | 1<<7
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !errors.Is(err, swd.ErrFault) {
		t.Fatalf("ReadAPIDR() error = %v, want typed FAULT", err)
	}
	if !fault.StateValid || fault.CTRLSTAT&(1<<4|1<<5|1<<7) != 1<<4|1<<5|1<<7 {
		t.Fatalf("FaultError = %+v, want captured sticky state", fault)
	}
	if got := target.abortValues[len(target.abortValues)-1]; got != 0x1e {
		t.Fatalf("sticky-clear ABORT = %#x, want 0x1e", got)
	}
	if target.sticky != 0 {
		t.Fatalf("sticky state after recovery = %#08x, want 0", target.sticky)
	}
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatalf("ReadAPIDR() after FAULT recovery: %v", err)
	}
}

func TestDebugPortUsesPreviousSELECTAfterWriteDataFAULT(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	selectReq := dpWrite(0x08)
	target.dropWriteFor = selectReq
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0xf0); err != nil {
		t.Fatal(err)
	}
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !fault.StateValid || fault.CTRLSTAT&testWriteDataError == 0 {
		t.Fatalf("ReadAPIDR() error = %v, want WDATAERR after abandoned SELECT data", err)
	}
	value, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if err != nil {
		t.Fatalf("ReadAPIDR() after WDATAERR recovery: %v", err)
	}
	if value.Raw != 0x24770011 {
		t.Fatalf("APIDR = %#08x, want %#08x", value.Raw, uint32(0x24770011))
	}
	if got := target.executed[selectReq]; got != 3 {
		t.Fatalf("executed SELECT writes = %d, want bootstrap, abandoned write, and restored AP selection", got)
	}
}

func TestDebugPortDoesNotReadCTRLSTATAfterAbandonedBankZeroSELECT(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err != nil {
		t.Fatal(err)
	}
	target.dropWriteFor = dpWrite(0x08)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0); err != nil {
		t.Fatal(err)
	}
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	var fault *dap.FaultError
	if !errors.As(err, &fault) || fault.StateValid || !strings.Contains(err.Error(), "cannot read CTRL/STAT") {
		t.Fatalf("ReadAPIDR() error = %v, want FAULT without CTRL/STAT state", err)
	}
	before := len(target.requests)
	assertRepairBlocksTraffic(t, dp, target, before)
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after abandoned bank-zero SELECT: %v", err)
	}
}

func TestDebugPortKeepsSELECTProvisionalAfterDPIDRRead(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.dropWriteFor = dpWrite(0x08)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || !strings.Contains(err.Error(), "bank is ambiguous") {
		t.Fatalf("ReadDP(CTRLSTAT) error = %v, want ambiguous bank", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after ambiguous CTRL/STAT read = %d, want %d", got, before)
	}
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, overrunDetect); err == nil || !strings.Contains(err.Error(), "bank is ambiguous") {
		t.Fatalf("WriteDP(CTRLSTAT) error = %v, want ambiguous bank", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after ambiguous CTRL/STAT write = %d, want %d", got, before)
	}
}

func TestConnectConfirmsSELECTThroughRDBUFF(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	selectIndex := -1
	for i, req := range target.requests {
		if req == (dpWrite(0x08)) {
			selectIndex = i
			break
		}
	}
	if selectIndex < 0 || selectIndex+2 >= len(target.requests) {
		t.Fatalf("connection requests = %#v, want SELECT, RDBUFF, CTRL/STAT", target.requests)
	}
	if target.requests[selectIndex+1] != (dpRead(0x0c)) || target.requests[selectIndex+2] != (dpRead(0x04)) {
		t.Fatalf("requests after SELECT = %#v, want RDBUFF then CTRL/STAT", target.requests[selectIndex:selectIndex+3])
	}
}

func TestConnectRepairsFailedRDBUFFConfirmation(t *testing.T) {
	tests := []struct {
		name string
		arm  func(*waitTarget, swdsim.Request)
		want error
	}{
		{name: "WAIT", arm: func(target *waitTarget, req swdsim.Request) { target.arm(req, 1) }, want: swd.ErrWait},
		{name: "FAULT", arm: func(target *waitTarget, req swdsim.Request) { target.armFault(req) }, want: swd.ErrFault},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newWaitTarget()
			wire := &reentryFailWire{inner: swdsim.New(target)}
			conn := swd.New(wire)
			dp := dap.NewDebugPort(conn)
			rdbuff := dpRead(0x0c)
			test.arm(target, rdbuff)

			if _, err := dp.Connect(t.Context()); !errors.Is(err, test.want) {
				t.Fatalf("Connect() error = %v, want %v", err, test.want)
			}
			if wire.reentryAttempts != 1 {
				t.Fatalf("SWD re-entry attempts = %d, want 1", wire.reentryAttempts)
			}
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatalf("Connect() after repaired SELECT confirmation: %v", err)
			}
		})
	}
}

func TestDebugPortRejectsAmbiguousCTRLSTATReadBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.dropWriteFor = dpWrite(0x08)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || !strings.Contains(err.Error(), "bank is ambiguous") {
		t.Fatalf("ReadDP(CTRLSTAT) error = %v, want ambiguous bank", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after ambiguous CTRL/STAT read = %d, want %d", got, before)
	}
}

func TestDebugPortReestablishesSELECTAfterABORT(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	selectReq := dpWrite(0x08)
	target.dropWriteFor = selectReq
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.ABORT, 1<<3); err != nil {
		t.Fatal(err)
	}
	value, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if err != nil {
		t.Fatalf("ReadAPIDR() after WDERRCLR: %v", err)
	}
	if value.Raw != 0x24770011 {
		t.Fatalf("APIDR = %#08x, want %#08x", value.Raw, uint32(0x24770011))
	}
	if got := target.executed[selectReq]; got != 3 {
		t.Fatalf("executed SELECT writes = %d, want bootstrap, abandoned write, and re-established AP selection", got)
	}
}

func TestDebugPortRequiresRepairWhenStickyClearDoesNotTakeEffect(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatal(err)
	}

	target.sticky = testWriteDataError
	target.dropStickyClear = true
	_, err = dp.ReadAPIDR(t.Context(), apSel(0))
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !errors.Is(err, swd.ErrFault) || !strings.Contains(err.Error(), "sticky state remains") {
		t.Fatalf("ReadAPIDR() error = %v, want FAULT and uncleared sticky state", err)
	}
	if !fault.StateValid || fault.CTRLSTAT&testWriteDataError == 0 {
		t.Fatalf("FaultError = %+v, want captured WDATAERR", fault)
	}
	before := len(target.requests)
	assertRepairBlocksTraffic(t, dp, target, before)
	if err := dp.Release(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("first Release() error = %v, want pending FAULT", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("retried Release(): %v", err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatalf("Connect() after repair: %v", err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("ReadWord() through pre-FAULT MEM-AP error = %v, want invalidated state", err)
	}
}

func TestDebugPortPreservesFAULTWhenCleanupFails(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	cleanupErr := errors.New("injected FAULT cleanup failure")
	target.sticky = testWriteDataError
	target.clearErr = cleanupErr
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !errors.Is(err, swd.ErrFault) || !errors.Is(err, cleanupErr) {
		t.Fatalf("ReadAPIDR() error = %v, want typed FAULT and cleanup failure", err)
	}
	if !fault.StateValid || fault.CTRLSTAT&testWriteDataError == 0 {
		t.Fatalf("FaultError = %+v, want captured WDATAERR", fault)
	}
	before := len(target.requests)
	assertRepairBlocksTraffic(t, dp, target, before)
	target.clearErr = nil
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after FAULT cleanup failure: %v", err)
	}
}

func TestDebugPortReportsFAULTWithoutStateWhenCTRLSTATReadFails(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	readErr := errors.New("injected CTRL/STAT read failure")
	target.sticky = testWriteDataError
	target.ctrlStatErr = readErr
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !errors.Is(err, swd.ErrFault) || !errors.Is(err, readErr) {
		t.Fatalf("ReadAPIDR() error = %v, want typed FAULT and CTRL/STAT read failure", err)
	}
	if fault.StateValid {
		t.Fatalf("FaultError = %+v, want StateValid false", fault)
	}
	before := len(target.requests)
	assertRepairBlocksTraffic(t, dp, target, before)
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after failed CTRL/STAT read: %v", err)
	}
	if target.sticky != 0 {
		t.Fatalf("sticky state after repair = %#08x, want 0", target.sticky)
	}
}

func TestDebugPortKeepsFramingAfterWAITThenFAULT(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	req := apRead(0x0c)
	target.arm(req, 1)
	target.fault = true
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("ReadAPIDR() error = %v, want %v", err, swd.ErrFault)
	}
	if target.attempts != 2 || target.executed[req] != 0 {
		t.Fatalf("WAIT-to-FAULT request attempted %d times and executed %d times", target.attempts, target.executed[req])
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("ReadWord() through invalidated MEM-AP error = %v", err)
	}
	mem, err = dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatalf("OpenMemAP() after clean FAULT: %v", err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatalf("ReadWord() through replacement MEM-AP: %v", err)
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, clean FAULT must not cause DAPABORT", target.abortValues)
		}
	}
}

func TestDebugPortPreservesWAITAndAbortFailure(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	abortErr := errors.New("injected DAPABORT failure")
	target.abortErr = abortErr
	target.arm(apRead(0x0c), -1)
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, abortErr) {
		t.Fatalf("ReadAPIDR() error = %v, want WAIT and abort failure", err)
	}
}

func TestDebugPortDoesNotReplayAfterWAITCleanupFailure(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	cleanupErr := errors.New("injected WAIT cleanup failure")
	wire := &cleanupFailWire{inner: swdsim.New(target), err: cleanupErr}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	req := apRead(0x0c)
	target.arm(req, 1)
	wire.armed = true
	_, err = dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, cleanupErr) {
		t.Fatalf("ReadAPIDR() error = %v, want WAIT and cleanup failure", err)
	}
	if !strings.Contains(err.Error(), "AP state is unknown") {
		t.Fatalf("ReadAPIDR() error = %v, want unknown AP state", err)
	}
	if target.attempts != 1 || target.executed[req] != 0 {
		t.Fatalf("request attempted %d times and executed %d times", target.attempts, target.executed[req])
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, DAPABORT is unsafe after incomplete cleanup", target.abortValues)
		}
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("ReadWord() after incomplete WAIT cleanup error = %v", err)
	}
}

func TestMEMAPReleaseReentersAfterDPWAITCleanupFailure(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	cleanupErr := errors.New("injected WAIT cleanup failure")
	wire := &cleanupFailWire{inner: swdsim.New(target), err: cleanupErr}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatal(err)
	}

	req := dpWrite(0x08)
	target.arm(req, 1)
	wire.armed = true
	_, err = dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, cleanupErr) {
		t.Fatalf("ReadAPIDR() error = %v, want WAIT and cleanup failure", err)
	}
	assertMEMAPInvalidated(t, mem)
	reentryRequest := -1
	wire.onReentry = func() { reentryRequest = len(target.requests) }
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := mem.Release(canceled); err != nil {
		t.Fatalf("Release() after SWD framing loss: %v", err)
	}
	if wire.trafficBeforeReentry != 0 {
		t.Fatalf("framed transfers before SWD re-entry = %d, want 0", wire.trafficBeforeReentry)
	}
	if wire.reentries != 1 {
		t.Fatalf("SWD re-entry attempts during MEM-AP release = %d, want 1", wire.reentries)
	}
	if reentryRequest < 0 || reentryRequest >= len(target.requests) ||
		target.requests[reentryRequest] != (dpRead(0x00)) {
		t.Fatalf("request index after SWD re-entry = %d in %#v, want DPIDR read", reentryRequest, target.requests)
	}
	if reentryRequest+1 >= len(target.requests) ||
		target.requests[reentryRequest+1] != (dpWrite(0x00)) {
		t.Fatalf("requests after SWD re-entry = %#v, want DPIDR then ABORT", target.requests[reentryRequest:])
	}
	assertMEMAPInvalidated(t, mem)
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if wire.reentries != 1 {
		t.Fatalf("total SWD re-entry attempts = %d, want 1", wire.reentries)
	}
	if wire.trafficBeforeReentry != 0 {
		t.Fatalf("framed transfers before SWD re-entry = %d, want 0", wire.trafficBeforeReentry)
	}
}

func TestDebugPortInvalidatesAPStateWhenWAITRetryFails(t *testing.T) {
	tests := []struct {
		name string
		req  swdsim.Request
	}{
		{name: "SELECT", req: dpWrite(0x08)},
		{name: "AP request", req: apRead(0x0c)},
		{name: "RDBUFF", req: dpRead(0x0c)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newWaitTarget()
			addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
			if err != nil {
				t.Fatal(err)
			}

			retryErr := errors.New("injected retry I/O failure")
			target.arm(test.req, -1)
			if test.name == "RDBUFF" {
				target.waitSkip = 1
			}
			target.retryErr = retryErr
			_, err = dp.ReadAPIDR(t.Context(), apSel(0))
			if !errors.Is(err, swd.ErrWait) || !errors.Is(err, retryErr) {
				t.Fatalf("ReadAPIDR() error = %v, want WAIT and retry I/O failure", err)
			}
			if !strings.Contains(err.Error(), "AP state is unknown") {
				t.Fatalf("ReadAPIDR() error = %v, want unknown AP state", err)
			}
			for _, value := range target.abortValues {
				if value&1 != 0 {
					t.Fatalf("ABORT writes = %#v, DAPABORT is unsafe after retry I/O failure", target.abortValues)
				}
			}
			if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err == nil || !strings.Contains(err.Error(), "invalidated") {
				t.Fatalf("ReadWord() after retry I/O failure error = %v", err)
			}
		})
	}
}

func TestDebugPortStopsRecoveryAfterStickyCleanupFailure(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	cleanupErr := errors.New("injected sticky-clear failure")
	target.clearErr = cleanupErr
	target.stickyAfterAbort = testWriteDataError
	target.executed[dpWrite(0x08)] = 0
	target.arm(apRead(0x0c), -1)
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, cleanupErr) {
		t.Fatalf("ReadAPIDR() error = %v, want WAIT and sticky-clear failure", err)
	}
	selectReq := dpWrite(0x08)
	if target.executed[selectReq] != 1 {
		t.Fatalf("SELECT writes = %d, want no write after sticky cleanup failure", target.executed[selectReq])
	}
}

func TestDebugPortStopsRecoveryAfterStickyStateReadFailure(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	readErr := errors.New("injected sticky-state read failure")
	target.readErrAfterAbort = readErr
	selectReq := dpWrite(0x08)
	target.executed[selectReq] = 0
	target.arm(apRead(0x0c), -1)
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, readErr) {
		t.Fatalf("ReadAPIDR() error = %v, want WAIT and sticky-state read failure", err)
	}
	if target.executed[selectReq] != 1 {
		t.Fatalf("SELECT writes = %d, want no write after sticky-state read failure", target.executed[selectReq])
	}
}

func TestConnectRollsBackAmbiguousPowerRequestWrite(t *testing.T) {
	target := newWaitTarget()
	conn := swd.New(swdsim.New(target))
	dp := dap.NewDebugPort(conn)

	req := dpWrite(0x04)
	writeErr := errors.New("injected failure after power-request write was accepted")
	target.arm(req, 1)
	target.waitSkip = 1
	target.writeErrFor = req
	target.writeErr = writeErr
	target.writeErrSkip = 1
	_, err := dp.Connect(t.Context())
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, writeErr) {
		t.Fatalf("Connect() error = %v, want WAIT and accepted write failure", err)
	}
	state, err := target.Read(t.Context(), dpRead(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if state&(debugRequest|systemRequest) != 0 {
		t.Fatalf("power requests after rollback = %#08x, want 0", state)
	}
}

func TestConnectRollsBackAgainstCurrentSetupIdentity(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.dpidrOverride = 0x0ba01477
	req := dpWrite(0x04)
	writeErr := errors.New("injected failure after power-request write was accepted")
	target.arm(req, 1)
	target.waitSkip = 1
	target.writeErrFor = req
	target.writeErr = writeErr
	target.writeErrSkip = 1
	_, err := dp.Connect(t.Context())
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, writeErr) {
		t.Fatalf("Connect() error = %v, want WAIT and accepted write failure", err)
	}
	if strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Connect() rollback used the previous identity: %v", err)
	}
	state, readErr := target.Read(t.Context(), dpRead(0x04))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state&(debugRequest|systemRequest) != 0 {
		t.Fatalf("power requests after rollback = %#08x, want 0", state)
	}
	if info, ok := dp.Identity(); !ok || info.Raw != 0x2ba01477 {
		t.Fatalf("Identity() = %+v, %t after failed setup", info, ok)
	}
}

func TestConnectRepairsBeforeIdentifyingReplacementTarget(t *testing.T) {
	target := newWaitTarget()
	readErr := errors.New("injected DPIDR transfer failure")
	wire := &cleanupFailWire{inner: swdsim.New(target), err: readErr, failBits: 42}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}

	target.dpidrOverride = 0x0ba01477
	wire.armed = true
	_, err := dp.Connect(t.Context())
	if !errors.Is(err, readErr) {
		t.Fatalf("Connect() error = %v, want DPIDR transfer failure", err)
	}
	if strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Connect() repair used the released identity: %v", err)
	}
	if wire.reentries != 1 {
		t.Fatalf("SWD re-entries = %d, want 1", wire.reentries)
	}
	if info, ok := dp.Identity(); !ok || info.Raw != 0x2ba01477 {
		t.Fatalf("Identity() = %+v, %t after failed setup", info, ok)
	}

	info, err := dp.Connect(t.Context())
	if err != nil {
		t.Fatalf("Connect() after automatic repair: %v", err)
	}
	if info.Raw != 0x0ba01477 {
		t.Fatalf("DPIDR = %#08x, want 0x0ba01477", info.Raw)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after replacement connection: %v", err)
	}
}

func TestConnectRetainsAmbiguousPowerAfterFailedRollback(t *testing.T) {
	target := newWaitTarget()
	repairErr := errors.New("injected protocol re-entry failure")
	wire := &reentryFailWire{inner: swdsim.New(target), reentryErr: repairErr}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)

	req := dpWrite(0x04)
	writeErr := errors.New("injected accepted power-request write failure")
	target.arm(req, 1)
	target.waitSkip = 1
	target.writeErrFor = req
	target.writeErr = writeErr
	target.writeErrSkip = 1
	wire.failEntries = 1
	_, err := dp.Connect(t.Context())
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, writeErr) || !errors.Is(err, repairErr) {
		t.Fatalf("Connect() error = %v, want WAIT, write failure, and repair failure", err)
	}
	state, readErr := target.Read(t.Context(), dpRead(0x04))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state&(debugRequest|systemRequest) != debugRequest|systemRequest {
		t.Fatalf("power requests after failed rollback = %#08x", state)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after failed rollback: %v", err)
	}
	state, readErr = target.Read(t.Context(), dpRead(0x04))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state&(debugRequest|systemRequest) != 0 {
		t.Fatalf("power requests after repair = %#08x, want 0", state)
	}
}

func TestReleaseFailureAllowsCleanupOnly(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatal(err)
	}

	target.armFault(dpWrite(0x04))
	if err := dp.Release(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Release() error = %v, want FAULT", err)
	}
	before := len(target.requests)
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil || !strings.Contains(err.Error(), "cleanup is pending") {
		t.Fatalf("ReadDP() error while cleanup pending = %v", err)
	}
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err == nil || !strings.Contains(err.Error(), "cleanup is pending") {
		t.Fatalf("ReadAPIDR() error while cleanup pending = %v", err)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "cleanup is pending") {
		t.Fatalf("ReadWord() error while cleanup pending = %v", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after blocked operations = %d, want %d", got, before)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("MemAP.Release() while cleanup pending: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("retried debug-port Release(): %v", err)
	}
}

func TestReleaseRepairsAbandonedSELECTWithInheritedPower(t *testing.T) {
	target := newWaitTarget()
	if err := target.Target.Write(t.Context(), dpWrite(0x04), debugRequest|systemRequest); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); err != nil {
		t.Fatal(err)
	}
	target.dropWriteFor = dpWrite(0x08)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError

	if err := dp.Release(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Release() error = %v, want FAULT after abandoned SELECT data", err)
	}
	before := len(target.requests)
	assertRepairBlocksTraffic(t, dp, target, before)
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after SELECT repair: %v", err)
	}
	state, err := target.Target.Read(t.Context(), dpRead(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if state&(debugRequest|systemRequest) != debugRequest|systemRequest {
		t.Fatalf("inherited power requests after release = %#08x", state)
	}
}

func TestReentryRejectsChangedDebugPort(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	cleanupErr := errors.New("injected WAIT cleanup failure")
	wire := &cleanupFailWire{inner: swdsim.New(target), err: cleanupErr}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	req := apRead(0x0c)
	target.arm(req, 1)
	wire.armed = true
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); !errors.Is(err, cleanupErr) {
		t.Fatalf("ReadAPIDR() error = %v, want cleanup failure", err)
	}
	aborts := len(target.abortValues)
	target.dpidrOverride = 0x0ba01477
	if err := dp.Release(t.Context()); err == nil || !strings.Contains(err.Error(), "changed from") {
		t.Fatalf("Release() error after DPIDR changed = %v", err)
	}
	if len(target.abortValues) != aborts {
		t.Fatalf("ABORT writes after identity mismatch = %d, want %d", len(target.abortValues), aborts)
	}
	state, err := target.Target.Read(t.Context(), dpRead(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if state&(debugRequest|systemRequest) != debugRequest|systemRequest {
		t.Fatalf("power requests after identity mismatch = %#08x", state)
	}
	target.dpidrOverride = 0
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after restoring identity: %v", err)
	}
}

func TestDebugPortRetriesSELECTAfterDAPAbort(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	target.selectWaits = 2
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	selectReq := dpWrite(0x08)
	target.executed[selectReq] = 0
	target.arm(apRead(0x0c), -1)
	_, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if !errors.Is(err, swd.ErrWait) {
		t.Fatalf("ReadAPIDR() error = %v, want %v", err, swd.ErrWait)
	}
	if target.selectAttempts != 2 {
		t.Fatalf("post-abort SELECT WAITs = %d, want 2", target.selectAttempts)
	}
	if target.executed[selectReq] != 2 {
		t.Fatalf("SELECT writes = %d, want AP selection and post-abort restoration", target.executed[selectReq])
	}
}

func TestDebugPortAbortsWAITAfterContextCancellation(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	wire := &cleanupFailWire{inner: swdsim.New(target)}
	conn := swd.New(wire)
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	wire.cancel = cancel
	target.arm(apRead(0x0c), -1)
	wire.armed = true
	_, err := dp.ReadAPIDR(ctx, apSel(0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadAPIDR() error = %v, want context cancellation", err)
	}
	if len(target.abortValues) == 0 || target.abortValues[len(target.abortValues)-1] != 1 {
		t.Fatalf("ABORT writes = %#v, want DAPABORT", target.abortValues)
	}
}

func TestDAPAbortInvalidatesExistingMEMAP(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mem.Release(context.Background()); err != nil {
			t.Errorf("release MEM-AP: %v", err)
		}
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	target.arm(dpRead(0x0c), -1)
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("ReadWord() error = %v, want %v", err, swd.ErrWait)
	}
	_, err = mem.ReadWord(t.Context(), 0xe000ed00)
	if err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("ReadWord() after DAP abort error = %v", err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after DAP abort: %v", err)
	}
	assertMEMAPInvalidated(t, mem)
}

func assertMEMAPInvalidated(t *testing.T, mem *dap.MemAP) {
	t.Helper()
	_, err := mem.ReadWord(t.Context(), 0xe000ed00)
	if err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("ReadWord() error = %v, want invalidated handle", err)
	}
}

func TestMEMAPReleaseRearmsRestorationAfterDAPAbort(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 0xa5000051); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x04), 0x20000000); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mem.Release(context.Background()); err != nil {
			t.Errorf("release MEM-AP: %v", err)
		}
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	tarReq := apWrite(0x04 & 0x0c)
	before := target.executed[tarReq]
	target.arm(apWrite(0x00&0x0c), -1)
	if err := mem.Release(t.Context()); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("Release() error = %v, want WAIT", err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("retried Release(): %v", err)
	}
	if got := target.executed[tarReq] - before; got != 2 {
		t.Fatalf("TAR restoration writes = %d, want 2", got)
	}
}

func TestImmediateDAPAbortInvalidatesAndRearmsMEMAP(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x24770011, map[uint32]uint32{0xe000ed00: 0x410fc241})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	const (
		originalCSW = uint32(0xa5000051)
		originalTAR = uint32(0x20000000)
	)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), originalCSW); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x04), originalTAR); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatal(err)
	}

	cswReq := apWrite(0x00 & 0x0c)
	target.armFault(cswReq)
	if err := mem.Release(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("MemAP.Release() error = %v, want FAULT", err)
	}
	tarReq := apWrite(0x04 & 0x0c)
	before := target.executed[tarReq]
	if err := dp.WriteDP(t.Context(), dap.ABORT, 1); err != nil {
		t.Fatal(err)
	}
	assertMEMAPInvalidated(t, mem)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("MemAP.Release() after immediate DAPABORT: %v", err)
	}
	if got := target.executed[tarReq] - before; got != 1 {
		t.Fatalf("TAR restoration writes after immediate DAPABORT = %d, want 1", got)
	}
	assertAPRegister(t, dp, 0x00, originalCSW)
	assertAPRegister(t, dp, 0x04, originalTAR)
}
