package coresight_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/coresight"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestWalkThroughDebugSpace(t *testing.T) {
	target := sim.New(0x4c013477)
	for reg, value := range map[dap.DPRegister]uint32{dap.DPIDR1: 20, dap.BASEPTR0: 1} {
		if err := target.SetDPRegister(reg, value); err != nil {
			t.Fatal(err)
		}
	}
	root := memoryAt(0, 1)
	root.words[0] = 0x2003
	root.words[4] = 0
	child := memoryAt(0x2000, 9)
	child.words[0x2fbc] = 0x47700a17
	for _, memory := range []*componentMemory{root, child} {
		for address, value := range memory.words {
			if err := target.SetDebugWord(address, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseSimulation(t, dp.Release) })
	space := dp.DebugSpace()
	base, present, err := space.ReadDebugBase(t.Context())
	if err != nil || !present || base != 0 {
		t.Fatalf("root=%#x,%v,%v", base, present, err)
	}
	visits, err := coresight.Walk(t.Context(), space, base, walkLimits())
	if err != nil || len(visits) != 2 {
		t.Fatalf("walk=%+v,%v", visits, err)
	}
	arch, ok := visits[1].Component.Architecture()
	if !ok || arch.ID != 0x0a17 || visits[1].Component.Base != 0x2000 {
		t.Fatalf("AP=%+v", visits[1])
	}
	limits := walkLimits()
	limits.MaxComponents = 1
	visits, err = coresight.Walk(t.Context(), space, base, limits)
	if !errors.Is(err, coresight.ErrWalkLimit) || len(visits) != 1 {
		t.Fatalf("bounded walk=%+v,%v", visits, err)
	}
}
