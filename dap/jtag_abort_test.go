package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestJTAGExplicitABORTAndCanceledPreflight(t *testing.T) {
	for _, ir := range []int{4, 8} {
		m, wire, chain := jtagModelChain(t, ir, 0)
		dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
		if _, err := dp.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before := wire.calls
		if err := dp.WriteDP(ctx, dap.ABORT, 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled ABORT: %v", err)
		}
		if wire.calls != before || m.aborts != 0 {
			t.Fatal("canceled ABORT sent traffic")
		}
		if err := dp.WriteDP(t.Context(), dap.ABORT, 1); err != nil {
			t.Fatal(err)
		}
		if m.aborts != 1 {
			t.Fatalf("ABORT executions = %d", m.aborts)
		}
		if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
			t.Fatal(err)
		}
		if err := dp.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if m.aborts != 1 {
			t.Fatal("release repeated the completed ABORT")
		}
	}
}

func TestJTAGFailedABORTRetainsCleanupObligation(t *testing.T) {
	m, wire, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	wire.fail = true
	if err := dp.WriteDP(t.Context(), dap.ABORT, 1); err == nil {
		t.Fatal("missing injected ABORT failure")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.aborts != 1 {
		t.Fatalf("recovered ABORT executions = %d", m.aborts)
	}
	before := wire.calls
	if err := dp.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatal("completed cleanup was repeated")
	}
}
