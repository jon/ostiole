package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/jtag"
)

func jtagReleaseCompletionFixture(t *testing.T) (*dap.DebugPort, *armJTAGBench) {
	t.Helper()
	_, wire, layout := jtagModelChain(t, 4, 0)
	bench := &armJTAGBench{jtagDPWire: wire}
	chain, err := jtag.NewChain(jtag.New(bench), layout.Layout())
	if err != nil {
		t.Fatal(err)
	}
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	return dp, bench
}

func TestCanceledFinalJTAGParkingClearsDAPOwnership(t *testing.T) {
	dp, bench := jtagReleaseCompletionFixture(t)
	before := bench.calls
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	finalCall := bench.calls - before
	dp, bench = jtagReleaseCompletionFixture(t)
	before = bench.calls
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bench.beforeTransfer = func(context.Context) error {
		if bench.calls+1 == before+finalCall {
			cancel()
		}
		return nil
	}
	if err := dp.Release(ctx); !errors.Is(err, context.Canceled) || dp.CleanupPending() {
		t.Fatalf("completed JTAG release retained ownership: %v", err)
	}
	before = bench.calls
	if err := dp.Release(t.Context()); err != nil || bench.calls != before {
		t.Fatalf("completed JTAG release repeated: %v", err)
	}
	for _, tap := range bench.taps {
		if tap.instruction != uint64(1)<<uint(tap.ir)-1 {
			t.Fatalf("TAP not parked: %#x", tap.instruction)
		}
	}
}
