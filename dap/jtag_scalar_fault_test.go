package dap_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
)

func TestJTAGScalarFaultDoesNotPermitWriteReplay(t *testing.T) {
	for _, size := range []dap.TransferSize{dap.Size8, dap.Size16, dap.Size32} {
		t.Run(fmt.Sprint(size), func(t *testing.T) { exerciseJTAGScalarFault(t, size) })
	}
}

func exerciseJTAGScalarFault(t *testing.T, size dap.TransferSize) {
	t.Helper()
	m, wire, chain := jtagModelChain(t, 4, 0)
	target := sim.New(0x2ba01477)
	sel := dap.NewAPSel(1)
	if err := target.AddMEMAP(sel, 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	shareAPModel(t, m, target)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	complete, effects := m.complete, 0
	m.complete = func(r jtagRequest) uint32 {
		value := complete(r)
		if !r.read && r.addr == 12 && m.selectDP&0xf0 == 0 {
			effects++
			m.ctrl |= 1 << 5
		}
		return value
	}
	err = mem.WriteScalar(t.Context(), 0x100, size, 42)
	if !errors.Is(err, dap.ErrFault) || !errors.Is(err, dap.ErrIndeterminate) || effects != 1 {
		t.Fatalf("scalar write: effects=%d, error=%v", effects, err)
	}
	assertJTAGMEMAPInvalid(t, mem, wire)
	if got, err := target.MEMAPBytes(sel, 0x100, 1); err != nil || got[0] != 42 {
		t.Fatalf("accepted write was not applied: %v, %v", got, err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
