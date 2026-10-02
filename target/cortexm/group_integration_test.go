//go:build integration

package cortexm_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/target/cortexm"
)

func TestHILRP2350Group(t *testing.T) {
	if os.Getenv("OSTIOLE_RP2350_HIL_GROUP") != "1" {
		t.Skip("require OSTIOLE_RP2350_HIL_GROUP=1")
	}
	if os.Getenv("OSTIOLE_RP2350_HIL_PROGRAM") != dualCounterProgram {
		t.Fatal("require the documented two-core counter binary identity")
	}
	for range 2 {
		if !t.Run("session", groupHIL) {
			return
		}
	}
}

func groupHIL(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	bench := controlBench{name: "RP2350 group", provider: "jlink", serial: "000802011345"}
	c := bench.open(t, ctx)
	var cores [2]dualCoreBench
	var g *cortexm.Group
	t.Cleanup(func() { releaseGroupBench(t, g, c) })
	var members []cortexm.Member
	for i, base := range []uint64{0x2000, 0x4000} {
		ap, err := dap.APAt(base)
		if err != nil {
			t.Fatal(err)
		}
		cores[i].memory, err = c.OpenMemAP(ctx, ap)
		if err != nil {
			t.Fatal(err)
		}
		checkDualCoreBench(t, ctx, &cores[i], i)
		members = append(members, cortexm.Member{ID: cortexm.CoreID(i + 1), Memory: cores[i].memory})
	}
	checkDualCounters(t, ctx, &cores, "before group acquisition", [2]bool{})
	var err error
	g, err = cortexm.AcquireGroup(ctx, members)
	if err != nil {
		t.Fatal(err)
	}
	exerciseGroupHIL(t, ctx, g, &cores)
	if results, err := g.Release(ctx); err != nil {
		t.Fatalf("group release=%+v: %v", results, err)
	}
	for i := range cores {
		checkDualCoreRestored(t, ctx, &cores[i], i)
	}
	checkDualCounters(t, ctx, &cores, "group released", [2]bool{})
}

func exerciseGroupHIL(t *testing.T, ctx context.Context, g *cortexm.Group, cores *[2]dualCoreBench) {
	t.Helper()
	for i := range cores {
		id := cortexm.CoreID(i + 1)
		if results, err := g.Halt(ctx, id); err != nil || len(results) != 1 || !results[0].HaltOwned || results[0].State != cortexm.Halted {
			t.Fatalf("group halt=%+v: %v", results, err)
		}
		stopped := [2]bool{}
		stopped[i] = true
		view := groupViewHIL{group: g, id: id}
		checkDualCounters(t, ctx, cores, "selected core halted", stopped)
		readRegistersHIL(t, ctx, view)
		checkCounterStepAtHIL(t, ctx, view, cores[i].memory, i, uint32(0x20040026+i*0x40), uint32(0x20040000+i*4))
		checkDualCounters(t, ctx, cores, "selected core stepped", stopped)
		if results, err := g.Resume(ctx, id); err != nil || results[0].Skipped || results[0].State != cortexm.Running {
			t.Fatalf("group resume=%+v: %v", results, err)
		}
		checkDualCounters(t, ctx, cores, "selected core resumed", [2]bool{})
	}
	results, err := g.Halt(ctx, 2, 1)
	if err != nil || len(results) != 2 || results[0].ID != 1 || results[1].ID != 2 {
		t.Fatalf("group halt=%+v: %v", results, err)
	}
	checkDualCounters(t, ctx, cores, "group halted sequentially", [2]bool{true, true})
	checkCounterStepAtHIL(t, ctx, groupViewHIL{group: g, id: 1}, cores[0].memory, 2, 0x20040026, 0x20040000)
	checkDualCounters(t, ctx, cores, "core 0 stepped with peer halted", [2]bool{true, true})
	if results, err := g.Resume(ctx, 1); err != nil || results[0].Skipped {
		t.Fatalf("selective resume=%+v: %v", results, err)
	}
	checkDualCounters(t, ctx, cores, "only core 0 resumed", [2]bool{false, true})
}

type groupViewHIL struct {
	group *cortexm.Group
	id    cortexm.CoreID
}

func (v groupViewHIL) ReadRegister(ctx context.Context, reg cortexm.Register) (uint32, error) {
	return v.group.ReadRegister(ctx, v.id, reg)
}

func (v groupViewHIL) Step(ctx context.Context) error { return v.group.Step(ctx, v.id) }

func (v groupViewHIL) Halted(ctx context.Context) (bool, error) {
	results, err := v.group.Status(ctx, v.id)
	return err == nil && results[0].State == cortexm.Halted, err
}

func releaseGroupBench(t *testing.T, g *cortexm.Group, c *armdebug.Conn) {
	t.Helper()
	var err error
	for range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = g.Release(ctx)
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Errorf("group cleanup pending; retaining Arm owner: %v", err)
		return
	}
	for range 3 {
		if err = c.Close(); err == nil {
			t.Log("group released and shared Arm debug owner closed")
			return
		}
	}
	t.Errorf("shared Arm debug owner cleanup remains pending: %v", err)
}
