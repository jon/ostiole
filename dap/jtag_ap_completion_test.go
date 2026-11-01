package dap_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
)

func TestJTAGWritePostAccessCheckFailureIsIndeterminate(t *testing.T) {
	t.Run("raw", func(t *testing.T) { exerciseJTAGPostAccessFailure(t, false) })
	t.Run("scalar", func(t *testing.T) { exerciseJTAGPostAccessFailure(t, true) })
}

func exerciseJTAGPostAccessFailure(t *testing.T, scalar bool) {
	t.Helper()
	m, wire, chain := jtagModelChain(t, 4, 0)
	target := sim.New(0x2ba01477)
	sel := dap.NewAPSel(1)
	if err := target.AddMEMAP(sel, 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	shareAPModel(t, m, target)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	// Configure the data address through the managed owner before injecting failure.
	if _, err := mem.ReadScalar(t.Context(), 0x100, dap.Size32); err != nil {
		t.Fatal(err)
	}
	complete, effects := m.complete, 0
	m.complete = func(r jtagRequest) uint32 {
		value := complete(r)
		if !r.read && r.addr == 12 && m.selectDP&0xf0 == 0 {
			effects++
		}
		return value
	}
	checks := 0
	m.onAccept = func(r jtagRequest) {
		if effects == 1 && checks == 0 && !r.ap && r.read && r.addr == 12 {
			checks++
			m.waits = 1
		}
	}
	if scalar {
		err = mem.WriteScalar(t.Context(), 0x100, dap.Size32, 42)
	} else {
		err = dp.WriteRawAP(t.Context(), sel.Address(12), 42)
	}
	if !errors.Is(err, dap.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) || effects != 1 || checks != 1 {
		t.Fatalf("write: effects=%d, post-access checks=%d, error=%v", effects, checks, err)
	}
	m.onAccept = nil
	assertJTAGMEMAPInvalid(t, mem, wire)
	releaseAPCompletionOwners(t, mem, dp)
}

func releaseAPCompletionOwners(t *testing.T, mem *dap.MemAP, dp *dap.DebugPort) {
	t.Helper()
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
