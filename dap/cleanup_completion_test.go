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

type cleanupDeadlineTarget struct {
	*dapsim.Target
	overrunSeen bool
	delayed     bool
}

func (t *cleanupDeadlineTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := t.Target.Read(ctx, req)
	if req == dpRead(4) {
		if value&1 != 0 {
			t.overrunSeen = true
		}
		if t.overrunSeen && value&1 == 0 && !t.delayed {
			t.delayed = true
			time.Sleep(80 * time.Millisecond)
		}
		value |= 1 << 12
	}
	return value, err
}

func TestFailedConnectKeepsCompletedCleanupDeadline(t *testing.T) {
	target := &cleanupDeadlineTarget{Target: dapsim.New(0x2ba01477)}
	wire := &releaseContextWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)), dap.WithCleanupTimeout(20*time.Millisecond))
	_, err := dp.Connect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "transaction modes") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("entry and completed cleanup errors=%v", err)
	}
	if t.Context().Err() != nil || !target.delayed || target.OverrunDetectEnabled() || dp.CleanupPending() {
		t.Fatalf("cleanup pending=%v overrun=%v delayed=%v", dp.CleanupPending(), target.OverrunDetectEnabled(), target.delayed)
	}
	before := wire.calls
	if err := dp.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatalf("idle release=%v traffic=%d/%d", err, wire.calls, before)
	}
}
