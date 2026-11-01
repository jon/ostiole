package armdebug_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/target/cortexm"
	coresim "github.com/jon/ostiole/target/cortexm/sim"
)

type registerFaultDevice struct{ fail bool }

func (d *registerFaultDevice) Read(context.Context, uint64, []byte) error {
	if d.fail {
		return dapsim.ErrBusFault
	}
	return nil
}
func (d *registerFaultDevice) Write(context.Context, uint64, []byte) error {
	if d.fail {
		return dapsim.ErrBusFault
	}
	return nil
}

func TestRegisterGroupSurvivesManagedMemoryFault(t *testing.T) {
	b := &registerBench{bench: newBench()}
	cores := mapRegisterCores(t, b)
	faulty := &registerFaultDevice{fail: true}
	if err := b.target.MapMEMAPDevice(dap.NewAPSel(1), 0x100, 4, faulty); err != nil {
		t.Fatal(err)
	}
	owner, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	group, first := acquireRegisterGroup(t, owner)
	exerciseRegisterGroupFault(t, group, first, cores)
	if _, err := group.Resume(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := group.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, core := range cores {
		if core.Snapshot().DHCSR&3 != 0 {
			t.Fatalf("target state not restored: %#x", core.Snapshot().DHCSR)
		}
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if b.registerActivations != 1 || b.activations != 0 || b.closes != 1 {
		t.Fatalf("activation/close counts=%d/%d/%d", b.registerActivations, b.activations, b.closes)
	}
}

func mapRegisterCores(t *testing.T, b *registerBench) []*coresim.Core {
	t.Helper()
	var cores []*coresim.Core
	for i := range 2 {
		sel := dap.NewAPSel(uint8(i + 1))
		if err := b.target.AddMEMAP(sel, 0x24770011, nil); err != nil {
			t.Fatal(err)
		}
		core, err := coresim.New(coresim.Config{Profile: coresim.M0})
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range []uint64{0xe000ed00, 0xe000ed30, 0xe000edf0} {
			if err := b.target.MapMEMAPDevice(sel, address, 4, core); err != nil {
				t.Fatal(err)
			}
		}
		cores = append(cores, core)
	}
	return cores
}

func acquireRegisterGroup(t *testing.T, owner *armdebug.Conn) (*cortexm.Group, *dap.MemAP) {
	t.Helper()
	members := make([]cortexm.Member, 2)
	var first *dap.MemAP
	for i := range members {
		memory, err := owner.OpenMemAP(t.Context(), dap.NewAPSel(uint8(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		members[i] = cortexm.Member{ID: cortexm.CoreID(i + 1), Memory: memory}
		if i == 0 {
			first = memory
		}
	}
	group, err := cortexm.AcquireGroup(t.Context(), members)
	if err != nil {
		t.Fatal(err)
	}

	return group, first
}

func exerciseRegisterGroupFault(t *testing.T, group *cortexm.Group, first *dap.MemAP, cores []*coresim.Core) {
	t.Helper()
	if _, err := group.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	for _, core := range cores {
		if core.Snapshot().DHCSR&(1<<17) == 0 {
			t.Fatal("group failed to stop core")
		}
	}
	if _, err := first.ReadWord(t.Context(), 0x100); !errors.Is(err, dap.ErrFault) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("memory fault=%v", err)
	}
	statuses, err := group.Status(t.Context(), 1, 2)
	if err != nil || len(statuses) != 2 {
		t.Fatalf("group after cleared fault=%v,%v", statuses, err)
	}
	for _, status := range statuses {
		if status.State != cortexm.Halted || !status.HaltOwned {
			t.Fatalf("lost stopped target: %+v", status)
		}
	}
}
