package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestJTAGFailedSetupRetainsStickyCleanup(t *testing.T) {
	m, wire, chain := jtagModelChain(t, 4, 0)
	m.ctrl, m.stuckSticky = 0xf0000020, true
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err == nil {
		t.Fatal("connected with sticky state")
	}
	if err := dp.Release(t.Context()); err == nil {
		t.Fatal("discarded pending sticky cleanup")
	}
	m.stuckSticky = false
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.ctrl != 0xf0000000 {
		t.Fatalf("restored CTRL/STAT = %#x", m.ctrl)
	}
	before := wire.calls
	if err := dp.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatal("completed cleanup repeated")
	}
}
