package sim_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/probe"
	swdsim "github.com/jon/ostiole/swd/sim"
	"github.com/jon/ostiole/target/cortexm"
	"github.com/jon/ostiole/target/cortexm/sim"
)

type clockedProbe struct {
	*swdsim.Wire
	closes int
	fail   error
}

func (p *clockedProbe) SWD(context.Context, probe.SWDConfig) (probe.Wire, error) { return p, nil }
func (p *clockedProbe) Close() error                                             { p.closes++; return nil }
func (p *clockedProbe) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.fail != nil {
		return nil, p.fail
	}
	return p.Wire.SWDIO(ctx, direction, output, bits)
}

type composition struct {
	owner    *armdebug.Conn
	probe    *clockedProbe
	cores    []*sim.Core
	memories []*dap.MemAP
	group    *cortexm.Group
}

func compose(t *testing.T, p sim.Profile, inherited bool) *composition {
	t.Helper()
	dp := dapsim.New(0x2ba01477)
	b := &composition{}
	selectors := []dap.APSel{dap.NewAPSel(0), dap.NewAPSel(1)}
	id := uint32(0x24770011)
	if p == sim.M33 {
		dp = dapsim.New(0x4c013477)
		if err := dp.SetDPRegister(dap.DPIDR1, 20); err != nil {
			t.Fatal(err)
		}
		id = 0x34770008
		for i := range selectors {
			sel, err := dap.APAt(uint64(i+1) * 0x2000)
			if err != nil {
				t.Fatal(err)
			}
			selectors[i] = sel
		}
	}
	for i := range 2 {
		sel := selectors[i]
		if err := dp.AddMEMAP(sel, id, nil); err != nil {
			t.Fatal(err)
		}
		initial := sim.Snapshot{}
		if p == sim.M33 {
			initial.DHCSR = secure
		}
		if inherited && i == 1 {
			initial.DHCSR |= enable | halt | halted
		}
		core, err := sim.New(sim.Config{Profile: p, Initial: initial})
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range []uint64{cpuid, dfsr, dhcsr} {
			if err := dp.MapMEMAPDevice(sel, address, 4, core); err != nil {
				t.Fatal(err)
			}
		}
		b.cores = append(b.cores, core)
	}
	b.probe = &clockedProbe{Wire: swdsim.New(dp)}
	owner, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b.probe), armdebug.Config{Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 1_000_000})})
	if err != nil {
		t.Fatal(err)
	}
	b.owner = owner
	members := make([]cortexm.Member, 2)
	for i := range members {
		m, err := owner.OpenMemAP(t.Context(), selectors[i])
		if err != nil {
			t.Fatal(err)
		}
		b.memories = append(b.memories, m)
		members[i] = cortexm.Member{ID: cortexm.CoreID(i + 1), Memory: m}
	}
	b.group, err = cortexm.AcquireGroup(t.Context(), members)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGroupThroughSharedArmDAPSWDOwners(t *testing.T) {
	for _, p := range []sim.Profile{sim.M0, sim.M33} {
		for _, inherited := range []bool{false, true} {
			t.Run(fmt.Sprintf("profile%d/inherited%t", p, inherited), func(t *testing.T) { checkGroupOwnership(t, p, inherited) })
		}
	}
}

func checkGroupOwnership(t *testing.T, p sim.Profile, inherited bool) {
	t.Helper()

	b := compose(t, p, inherited)
	if _, err := b.group.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	result, err := b.group.Resume(t.Context(), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result[0].Skipped || result[1].Skipped != inherited {
		t.Fatalf("ownership outcome %+v", result)
	}
	if b.cores[0].Snapshot().DHCSR&halted != 0 || (b.cores[1].Snapshot().DHCSR&halted != 0) != inherited {
		t.Fatal("selected resume altered inherited halt")
	}
	if _, err := b.group.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.probe.closes != 0 {
		t.Fatal("group closed lower owner")
	}
	if err := b.owner.Close(); err != nil {
		t.Fatal(err)
	}
	if b.probe.closes != 1 {
		t.Fatal("shared probe not closed exactly once")
	}
	for i, c := range b.cores {
		expected := uint32(0)
		if inherited && i == 1 {
			expected = 3 | halted
		}
		if c.Snapshot().DHCSR&(3|halted) != expected {
			t.Fatalf("core %d not restored: %+v", i, c.Snapshot())
		}
	}
}

func TestSharedTransportFailureRetainsCleanup(t *testing.T) {
	b := compose(t, sim.M33, false)
	if _, err := b.group.Halt(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	before := b.cores[0].Snapshot()
	b.probe.fail = errors.New("lost shared wire")
	if _, err := b.group.Status(t.Context(), 2); !errors.Is(err, b.probe.fail) {
		t.Fatal(err)
	}
	if _, err := b.group.Release(t.Context()); err == nil {
		t.Fatal("group released through failed transport")
	}
	if err := b.owner.Close(); err == nil || b.probe.closes != 0 {
		t.Fatalf("owner discarded dependencies: %v", err)
	}
	if b.cores[0].Snapshot() != before {
		t.Fatal("failed shared transport resumed peer")
	}
	if result := b.group.Results(); !result[0].CleanupPending || !result[1].CleanupPending {
		t.Fatal("cleanup obligations lost")
	}
}
