package swd_test

import (
	"context"
	"errors"
	"testing"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type cancelCompletedCTRLTarget struct {
	*dapsim.Target
	cancel      context.CancelFunc
	wantOverrun bool
}

func (t *cancelCompletedCTRLTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := t.Target.Read(ctx, req)
	if err == nil && !req.AP && req.Read && req.Addr == 4 && t.cancel != nil && (value&1 != 0) == t.wantOverrun {
		t.cancel()
		t.cancel = nil
	}
	return value, err
}

func TestCancellationInFinalSWDLifecycleFrame(t *testing.T) {
	for _, connect := range []bool{false, true} {
		target := &cancelCompletedCTRLTarget{Target: dapsim.New(0x2ba01477)}
		wire := &entryFailureWire{inner: swdsim.New(target)}
		conn := swd.New(wire)
		ctx, cancel := context.WithCancel(t.Context())
		if connect {
			target.cancel, target.wantOverrun = cancel, true
			_, err := conn.Connect(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Connect final frame: %v", err)
			}
		} else {
			if _, err := conn.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			target.cancel = cancel
			if err := conn.Release(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Release final frame: %v", err)
			}
		}
		cancel()
		if conn.CleanupPending() || target.OverrunDetectEnabled() {
			t.Fatal("completed cleanup retained ownership or mode")
		}
		before := wire.calls
		if err := conn.Release(t.Context()); err != nil || before != wire.calls {
			t.Fatalf("completed restoration replayed: %v", err)
		}
	}
}
