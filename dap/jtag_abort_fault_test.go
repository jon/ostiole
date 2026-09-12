package dap_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestJTAGAbortChecksRacingStickyFault(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(map[bool]string{false: "clear", true: "retry cleanup"}[stuck], func(t *testing.T) {
			m, _, chain := jtagModelChain(t, 4, 0)
			m.ctrl = 0xf0000000
			dp := dap.NewDebugPort(dap.JTAGDP(chain, 0), dap.WithMaxWaits(2))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			m.nextWaits = 10
			m.onAbort = func() { m.ctrl |= 1 << 5 }
			m.stuckSticky = stuck
			_, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
			if !errors.Is(err, dap.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) || m.aborts != 1 {
				t.Fatalf("aborted AP: aborts=%d, error=%v", m.aborts, err)
			}
			if stuck {
				if err := dp.Release(t.Context()); err == nil {
					t.Fatal("released with abort fault cleanup pending")
				}
			} else {
				m.complete = func(jtagRequest) uint32 { return 0x24770011 }
				if _, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1)); err != nil {
					t.Fatalf("next AP operation inherited abort fault: %v", err)
				}
			}
			m.stuckSticky = false
			if err := dp.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if m.ctrl != 0xf0000000 || m.aborts != 1 {
				t.Fatalf("cleanup: CTRL/STAT=%#x, aborts=%d", m.ctrl, m.aborts)
			}
		})
	}
}
