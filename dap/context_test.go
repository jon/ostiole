package dap_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestNilContextPreservesDebugPortState(t *testing.T) {
	for _, link := range []string{"SWD", "JTAG"} {
		t.Run(link, func(t *testing.T) { exerciseNilContext(t, link) })
	}
}

func exerciseNilContext(t *testing.T, link string) {
	t.Helper()
	target := sim.New(0x2ba01477)
	sel := dap.NewAPSel(1)
	if err := target.AddMEMAP(sel, 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	wire := &cleanupBudgetWire{inner: swdsim.New(target)}
	port := dap.SWDP(swd.New(wire))
	calls := func() int { return wire.calls }
	if link == "JTAG" {
		model, jwire, chain := jtagModelChain(t, 4, 0)
		shareAPModel(t, model, target)
		port = dap.JTAGDP(chain, 0)
		calls = func() int { return jwire.calls }
	}
	dp := dap.NewDebugPort(port)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	operations := nilContextOperations(dp, mem, sel, link)
	for _, name := range slices.Sorted(maps.Keys(operations)) {
		t.Run(name, func(t *testing.T) {
			before := calls()
			if err := operations[name](); err == nil {
				t.Fatal("nil context succeeded")
			}
			if calls() != before {
				t.Fatal("nil context sent traffic")
			}
			if _, err := mem.ReadWord(t.Context(), 0); err != nil {
				t.Fatalf("nil context invalidated memory access: %v", err)
			}
		})
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func nilContextOperations(dp *dap.DebugPort, mem *dap.MemAP, sel dap.APSel, link string) map[string]func() error {
	var ctx context.Context
	identity := dap.DPIDR
	if link == "JTAG" {
		identity = dap.IDCODE
	}
	return map[string]func() error{
		"identity":       func() error { _, err := dp.ReadDP(ctx, identity); return err },
		"read DP":        func() error { _, err := dp.ReadDP(ctx, dap.CTRLSTAT); return err },
		"write DP":       func() error { return dp.WriteDP(ctx, dap.SELECT, 0) },
		"abort":          func() error { return dp.WriteDP(ctx, dap.ABORT, 1) },
		"read AP":        func() error { _, err := dp.ReadRawAP(ctx, sel.Address(0)); return err },
		"write AP":       func() error { return dp.WriteRawAP(ctx, sel.Address(0), 0) },
		"read memory":    func() error { _, err := mem.ReadWord(ctx, 0); return err },
		"write memory":   func() error { return mem.WriteScalar(ctx, 0, dap.Size32, 0) },
		"read block":     func() error { _, err := mem.ReadBlock(ctx, 0, make([]byte, 4)); return err },
		"write block":    func() error { _, err := mem.WriteBlock(ctx, 0, make([]byte, 4)); return err },
		"release memory": func() error { return mem.Release(ctx) },
		"release DP":     func() error { return dp.Release(ctx) },
		"transaction": func() error {
			txn := dp.NewTxn()
			read := txn.ReadDP(dap.CTRLSTAT)
			write := txn.WriteRawAP(sel.Address(0), 0)
			err := txn.Commit(ctx)
			_, readErr := read.Value()
			if readErr == nil || write.Err() == nil {
				return nil
			}
			return err
		},
	}
}

func TestReleasePreflightContextPreservesConnectedPort(t *testing.T) {
	for _, link := range []string{"SWD", "JTAG"} {
		for _, failure := range []string{"canceled", "expired"} {
			t.Run(link+"/"+failure, func(t *testing.T) {
				checkReleasePreflightContext(t, link, failure)
			})
		}
	}
}

func checkReleasePreflightContext(t *testing.T, link, failure string) {
	t.Helper()
	wire := &cleanupBudgetWire{inner: swdsim.New(sim.New(0x2ba01477))}
	port := dap.SWDP(swd.New(wire))
	calls := func() int { return wire.calls }
	if link == "JTAG" {
		_, jwire, chain := jtagModelChain(t, 4, 0)
		port = dap.JTAGDP(chain, 0)
		calls = func() int { return jwire.calls }
	}
	dp := dap.NewDebugPort(port)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if failure == "expired" {
		cancel()
		ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	}
	cancel()
	before := calls()
	if err := dp.Release(ctx); !errors.Is(err, ctx.Err()) || calls() != before {
		t.Fatalf("Release = %v, wire calls = %d; want %d", err, calls(), before)
	}
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
		t.Fatalf("preflight context invalidated connected port: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
