package sim_test

import (
	"context"
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
	for i := range selectors {
		m, err := owner.OpenMemAP(t.Context(), selectors[i])
		if err != nil {
			t.Fatal(err)
		}
		b.memories = append(b.memories, m)
	}
	return b
}

func TestIdentityThroughSharedArmDAPSWDOwners(t *testing.T) {
	for _, p := range []sim.Profile{sim.M0, sim.M33} {
		b := compose(t, p, false)
		for i, m := range b.memories {
			before := b.cores[i].Snapshot()
			identity, err := cortexm.Identify(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			want := uint32(0x410cc200)
			if p == sim.M33 {
				want = 0x411fd210
			}
			if identity.Raw != want || b.cores[i].Snapshot() != before {
				t.Fatal("identity changed debug state")
			}
		}
		if err := b.owner.Close(); err != nil || b.probe.closes != 1 {
			t.Fatalf("shared owner cleanup: %v", err)
		}
	}
}
