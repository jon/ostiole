package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestDebugPortCleanupPendingTracksRealSession(t *testing.T) {
	var absent *dap.DebugPort
	if absent.CleanupPending() || new(dap.DebugPort).CleanupPending() {
		t.Fatal("empty port owns cleanup")
	}
	wire := &cleanupBudgetWire{inner: swdsim.New(sim.New(0x2ba01477))}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if dp.CleanupPending() || wire.calls != 0 {
		t.Fatal("constructor/query caused effects")
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	if !dp.CleanupPending() || wire.calls != before {
		t.Fatal("active session query")
	}
	wire.fail = true
	if err := dp.Release(t.Context()); err == nil || !dp.CleanupPending() {
		t.Fatalf("failed release lost cleanup: %v", err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if dp.CleanupPending() {
		t.Fatal("released port owns cleanup")
	}
}

func TestFailedUnownedEntryHasNoDAPCleanup(t *testing.T) {
	wire := &cleanupBudgetWire{inner: swdsim.New(sim.New(0x2ba01477)), fail: true}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err == nil {
		t.Fatal("missing entry failure")
	}
	before := wire.calls
	if dp.CleanupPending() || wire.calls != before {
		t.Fatal("unowned entry acquired cleanup")
	}
}
