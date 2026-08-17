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
	if err := conn.JTAGToSWD(ctx); err != nil {
		t.Fatal(err)
	}
	dp := dap.NewSWDP(conn)

	value, err := dp.ReadDP(ctx, dap.DPIDR)
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x2ba01477 {
		t.Fatalf("DPIDR = %#08x, want 0x2ba01477", value)
	}

	const requests = uint32(1<<28 | 1<<30)
	if err := dp.WriteDP(ctx, dap.CTRLSTAT, requests); err != nil {
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

func TestNilDebugPortAccess(t *testing.T) {
	dp := dap.NewSWDP(nil)
	if _, err := dp.ReadDP(context.Background(), dap.DPIDR); err == nil {
		t.Fatal("ReadDP() succeeded without an SWD connection")
	}
	if err := dp.WriteDP(context.Background(), dap.ABORT, 0); err == nil {
		t.Fatal("WriteDP() succeeded without an SWD connection")
	}
}
