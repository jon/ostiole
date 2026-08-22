package dap_test

import (
	"context"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestImmediateDebugPortAccess(t *testing.T) {
	ctx := context.Background()
	target := sim.New(0x2ba01477)
	conn := swd.New(swdsim.New(target))
	dp := dap.NewDebugPort(conn)
	if _, err := dp.Connect(ctx); err != nil {
		t.Fatal(err)
	}

	value, err := dp.ReadDP(ctx, dap.DPIDR)
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x2ba01477 {
		t.Fatalf("DPIDR = %#08x, want 0x2ba01477", value)
	}

	const requests = uint32(1<<28 | 1<<30)
	state, err := dp.ReadDP(ctx, dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(ctx, dap.CTRLSTAT, state|requests); err != nil {
		t.Fatal(err)
	}
	value, err = dp.ReadDP(ctx, dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	if value&requests != requests {
		t.Fatalf("CTRL/STAT = %#08x, want request bits %#08x", value, requests)
	}
}

func TestDebugPortOperationsRequireConnect(t *testing.T) {
	target := newWaitTarget()
	wire := &entryFailureWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(swd.New(wire))
	before := wire.calls
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil {
		t.Fatal("ReadDP() succeeded before Connect()")
	}
	if err := dp.WriteDP(t.Context(), dap.ABORT, 0); err == nil {
		t.Fatal("WriteDP() succeeded before Connect()")
	}
	if wire.calls != before {
		t.Fatalf("operations before Connect() sent %d wire calls", wire.calls-before)
	}
}

func TestNilDebugPortAccess(t *testing.T) {
	dp := dap.NewDebugPort(nil)
	if _, err := dp.ReadDP(context.Background(), dap.DPIDR); err == nil {
		t.Fatal("ReadDP() succeeded without an SWD connection")
	}
	if err := dp.WriteDP(context.Background(), dap.ABORT, 0); err == nil {
		t.Fatal("WriteDP() succeeded without an SWD connection")
	}
}
