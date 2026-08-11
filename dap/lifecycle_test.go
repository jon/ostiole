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

func TestConnectAndReleaseSWDP(t *testing.T) {
	dp := enteredDP(t, sim.New(0x2ba01477))
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
	assertPower(t, dp, 0)
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
	dp := enteredDP(t, sim.New(0x2ba01477))
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, debugRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertPower(t, dp, debugRequest|debugAck)
}

func TestConnectRollsBackAfterAcknowledgementTimeout(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477, suppressAck: true}
	dp := enteredDP(t, target)
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
	dp := enteredDP(t, sim.New(0x2ba01477))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err == nil {
		t.Fatal("second Connect() succeeded")
	}
}

func TestReleaseCanRetryAfterWriteFailure(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477, failRelease: true}
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err == nil {
		t.Fatal("Release() succeeded despite the injected write failure")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("retried Release() failed: %v", err)
	}
	if target.ctrl&allPower != 0 {
		t.Fatalf("power state after retry = %#08x, want 0", target.ctrl)
	}
}

func TestConnectClearsStickyStateBeforeSelecting(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477, sticky: true, ackAfter: 2}
	dp := enteredDP(t, target)
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
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(target.abortValues) == 0 || target.abortValues[0] != 0x1c {
		t.Fatalf("ABORT writes = %#v, want minimal-DP sticky clear 0x1c", target.abortValues)
	}
}

func TestConnectReadsIdentityBeforeConfiguration(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477}
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectRejectsEnabledOverrunDetectionBeforeConfiguration(t *testing.T) {
	target := &powerTarget{
		dpidr:  0x2ba01477,
		ctrl:   overrunDetect,
		dpBank: 1,
	}
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err == nil {
		t.Fatal("Connect() succeeded with ORUNDETECT enabled")
	}
	if len(target.selectValues) != 2 || target.selectValues[0] != 0 || target.selectValues[1] != 0 {
		t.Fatalf("SELECT writes while rejecting and repairing ORUNDETECT = %#v, want two bank-zero writes", target.selectValues)
	}
	if len(target.abortValues) != 2 || target.abortValues[0] != 0x1e || target.abortValues[1] != 0x1e ||
		target.ctrl != overrunDetect {
		t.Fatalf("state after rejecting and repairing ORUNDETECT: ABORT=%#v CTRL/STAT=%#08x", target.abortValues, target.ctrl)
	}
}

func TestWriteDPRejectsEnablingOverrunDetection(t *testing.T) {
	target := &powerTarget{dpidr: 0x2ba01477}
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, state|overrunDetect); err == nil {
		t.Fatal("WriteDP() accepted unsupported ORUNDETECT")
	}
	if target.ctrl&overrunDetect != 0 {
		t.Fatalf("CTRL/STAT after rejected write = %#08x, want ORUNDETECT clear", target.ctrl)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("Release() after rejected write: %v", err)
	}
	if target.ctrl&allPower != 0 {
		t.Fatalf("power state after release = %#08x, want 0", target.ctrl)
	}
}

func enteredDP(t *testing.T, target swdsim.Target) *dap.DebugPort {
	t.Helper()
	conn := swd.New(swdsim.New(target))
	if err := conn.JTAGToSWD(t.Context()); err != nil {
		t.Fatal(err)
	}
	return dap.NewSWDP(conn)
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

func (t *powerTarget) Acknowledge(_ context.Context, req swd.Request) error {
	if !req.AP && !req.Read && req.Addr == uint8(dap.SELECT) && t.sticky {
		return swd.ErrFault
	}
	return nil
}

func (t *powerTarget) Read(_ context.Context, req swd.Request) (uint32, error) {
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

func (t *powerTarget) Write(_ context.Context, req swd.Request, value uint32) error {
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
