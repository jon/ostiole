package dap_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestExemptDPReadsSettleEarlierWrites(t *testing.T) {
	for _, reg := range []dap.DPRegister{dap.CTRLSTAT, dap.DPIDR} {
		for _, lost := range []bool{false, true} {
			t.Run(reg.String()+map[bool]string{false: "/deferred", true: "/lost"}[lost], func(t *testing.T) {
				checkExemptDPRead(t, reg, lost)
			})
		}
	}
}

func TestPowerWritesCompleteBeforePolling(t *testing.T) {
	for _, release := range []bool{false, true} {
		for _, lost := range []bool{false, true} {
			name := map[bool]string{false: "connect", true: "release"}[release] + map[bool]string{false: "/deferred", true: "/lost"}[lost]
			t.Run(name, func(t *testing.T) {
				checkPowerWrite(t, release, lost)
			})
		}
	}
}

func TestCanceledExemptReadRetainsPendingDPWrite(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.dropWriteFor, target.dropWrite, target.stickyOnDrop = dpWrite(0x04), true, testWriteDataError
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, overrunDetect|debugRequest); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(target.requests)
	if _, err := dp.ReadDP(ctx, dap.CTRLSTAT); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadDP() = %v, want cancellation", err)
	}
	if len(target.requests) != before {
		t.Fatal("canceled read sent traffic")
	}
	txn := dp.NewTxn()
	abort := txn.WriteDP(dap.ABORT, 1)
	if err := txn.Commit(t.Context()); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("Commit() = %v, want earlier write fault", err)
	}
	if err := abort.Err(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("queued ABORT = %v, want not executed", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type stagedPowerTarget struct {
	*powerTarget
	risingReads  int
	fallingReads int
	falling      bool
	observed     uint32
}

func (p *stagedPowerTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := p.powerTarget.Read(ctx, req)
	if err != nil || req != dpRead(4) {
		return value, err
	}
	if p.falling {
		p.fallingReads++
		if p.fallingReads < 4 {
			value |= systemAck
		}
	} else if value&(debugRequest|systemRequest) == debugRequest|systemRequest {
		p.risingReads++
		if p.risingReads < 6 {
			value &^= systemAck
		}
	}
	p.observed = value
	return value, nil
}

func (p *stagedPowerTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req == dpWrite(4) && p.ctrl&systemRequest != 0 && value&systemRequest == 0 {
		p.falling = true
	}
	return p.powerTarget.Write(ctx, req, value)
}

func TestPowerHandshakeWaitsForBothACKsIncludingInheritedRequests(t *testing.T) {
	for _, inherited := range []uint32{0, debugRequest, debugRequest | systemRequest} {
		t.Run(fmt.Sprintf("inherited-%x", inherited), func(t *testing.T) {
			target := &stagedPowerTarget{powerTarget: &powerTarget{dpidr: 0x2ba01477, ctrl: inherited}}
			if inherited&debugRequest != 0 {
				target.ctrl |= debugAck
			}
			if inherited&systemRequest != 0 {
				target.ctrl |= systemAck
			}
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			if target.observed&(debugAck|systemAck) != debugAck|systemAck {
				t.Fatalf("Connect returned with observed ACKs %#x", target.observed&(debugAck|systemAck))
			}
			if err := dp.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if inherited&systemRequest == 0 && (target.fallingReads < 4 || target.observed&systemAck != 0) {
				t.Fatalf("Release returned before owned ACK fell: reads=%d, CTRL/STAT=%#x", target.fallingReads, target.observed)
			}
			if target.ctrl&(debugRequest|systemRequest) != inherited {
				t.Fatalf("requests after Release = %#x, want %#x", target.ctrl, inherited)
			}
		})
	}
}

func TestExemptReadBarrierWAITRetainsCleanupForRetry(t *testing.T) {
	target := newWaitTarget()
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, overrunDetect|debugRequest); err != nil {
		t.Fatal(err)
	}
	target.arm(dpRead(12), -1)
	before := target.executed[dpRead(4)]
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("ReadDP() = %v, want WAIT", err)
	}
	assertDAPABORT(t, target)
	if target.executed[dpRead(4)] != before+1 {
		t.Fatal("want only the post-abort CTRL/STAT check, without an ordinary poll")
	}
	target.armed = false
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := target.Target.Read(t.Context(), dpRead(4))
	if err != nil {
		t.Fatal(err)
	}
	if state&allPower != 0 {
		t.Fatalf("actual power after retry = %#x", state&allPower)
	}
}

func checkExemptDPRead(t *testing.T, reg dap.DPRegister, lost bool) {
	t.Helper()

	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	target.deferWriteFor, target.deferWrite = dpWrite(0x04), !lost
	target.dropWriteFor, target.dropWrite = dpWrite(0x04), lost
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, overrunDetect|debugRequest); err != nil {
		t.Fatal(err)
	}
	before := target.executed[dpRead(0x0c)]
	_, err := dp.ReadDP(t.Context(), reg)
	if lost {
		var fault *dap.FaultError
		if !errors.As(err, &fault) || !fault.StateValid || fault.CTRLSTAT&testWriteDataError == 0 {
			t.Fatalf("ReadDP() error = %v, want captured WDATAERR", err)
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		state, err := target.Target.Read(t.Context(), dpRead(0x04))
		if err != nil {
			t.Fatal(err)
		}
		if state&allPower != debugRequest|debugAck {
			t.Fatalf("actual power = %#x, want debug only", state&allPower)
		}
		if target.executed[dpRead(0x0c)] == before {
			t.Fatal("read did not complete the deferred write through RDBUFF")
		}
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func checkPowerWrite(t *testing.T, release, lost bool) {
	t.Helper()

	target := newWaitTarget()
	target.SetOverrunDetect(true)
	dp := newDebugPort(t, target)
	if release {
		if _, err := dp.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	target.deferWriteFor, target.deferWrite = dpWrite(0x04), !lost
	target.dropWriteFor, target.dropWrite = dpWrite(0x04), lost
	target.stickyOnDrop = testWriteDataError
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	var err error
	if release {
		err = dp.Release(ctx)
	} else {
		_, err = dp.Connect(ctx)
	}
	if lost {
		var fault *dap.FaultError
		if !errors.As(err, &fault) || !fault.StateValid || fault.CTRLSTAT&testWriteDataError == 0 {
			t.Fatalf("power transition error = %v, want captured WDATAERR", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost write reached deadline: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := target.Target.Read(t.Context(), dpRead(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if state&allPower != 0 {
		t.Fatalf("actual power after cleanup = %#x", state&allPower)
	}
	before := len(target.requests)
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(target.requests) != before {
		t.Fatal("successful release repeated traffic")
	}
}
