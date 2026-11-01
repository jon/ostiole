package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

type stalledConnectionTarget struct {
	*connectionTarget
	stalled        bool
	stallSelect    bool
	changeIdentity bool
	abortErr       error
	aborts         int
	apRequests     int
}

func (t *stalledConnectionTarget) Acknowledge(ctx context.Context, req sim.Request) error {
	if req.AP {
		t.apRequests++
	}
	if t.stalled && (req.AP || t.stallSelect && req == (sim.Request{Addr: 8}) || req == (sim.Request{Read: true, Addr: 12})) {
		if t.changeIdentity {
			t.dpidr = 0x6ba02477
			t.changeIdentity = false
		}
		return swd.ErrWait
	}
	return t.connectionTarget.Acknowledge(ctx, req)
}

func (t *stalledConnectionTarget) Write(ctx context.Context, req sim.Request, value uint32) error {
	if req == (sim.Request{Addr: 0}) && value&1 != 0 {
		t.aborts++
		if t.abortErr != nil {
			return t.abortErr
		}
		t.stalled = false
	}
	return t.connectionTarget.Write(ctx, req, value)
}

func TestConnectAbortsInheritedStalledAP(t *testing.T) {
	for _, test := range []struct {
		name                string
		overrun, selectWait bool
	}{
		{"simple/SELECT", false, true}, {"overrun/SELECT", true, true},
		{"simple/RDBUFF", false, false}, {"overrun/RDBUFF", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := &stalledConnectionTarget{connectionTarget: &connectionTarget{dpidr: 0x2ba01477}, stalled: true, stallSelect: test.selectWait}
			if test.overrun {
				target.ctrlStat = 1
			}
			conn := swd.New(sim.New(target))
			if _, err := conn.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			if target.stalled || target.aborts != 1 || target.apRequests != 0 {
				t.Fatalf("stalled=%t aborts=%d AP requests=%d", target.stalled, target.aborts, target.apRequests)
			}
			if err := conn.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if (target.ctrlStat&1 != 0) != test.overrun || target.selectDP != 0 {
				t.Fatalf("restored CTRL/STAT=%#x SELECT=%#x", target.ctrlStat, target.selectDP)
			}
		})
	}
}

func TestStalledReleaseRetainsRetryAfterAbortFailure(t *testing.T) {
	target := &stalledConnectionTarget{connectionTarget: &connectionTarget{dpidr: 0x2ba01477}}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteDP(t.Context(), 8, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	target.stalled, target.stallSelect = true, true
	if err := conn.Release(t.Context()); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("Release() = %v, want WAIT", err)
	}
	target.abortErr = errors.New("injected abort failure")
	if err := conn.Release(t.Context()); !errors.Is(err, target.abortErr) {
		t.Fatalf("Release() = %v, want abort failure", err)
	}
	target.abortErr = nil
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.stalled || target.ctrlStat&1 != 0 || target.selectDP != 0 || target.apRequests != 0 {
		t.Fatalf("restoration stalled=%t CTRL/STAT=%#x SELECT=%#x AP=%d", target.stalled, target.ctrlStat, target.selectDP, target.apRequests)
	}
	before := target.aborts
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.aborts != before {
		t.Fatal("repeated successful release sent abort")
	}
}

func TestBootstrapRetryRejectsChangedIdentityBeforeAbort(t *testing.T) {
	target := &stalledConnectionTarget{
		connectionTarget: &connectionTarget{dpidr: 0x2ba01477},
		stalled:          true, stallSelect: true, changeIdentity: true,
	}
	conn := swd.New(sim.New(target))
	if id, err := conn.Connect(t.Context()); err == nil {
		t.Fatalf("changed identity accepted: %#x", id)
	}
	if target.aborts != 0 || target.ctrlWrites != 0 {
		t.Fatalf("changed device received aborts=%d control writes=%d", target.aborts, target.ctrlWrites)
	}
	before := target.aborts
	target.dpidr = 0x2ba01477
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !target.stalled || target.aborts != before {
		t.Fatal("unowned release changed inherited stalled work")
	}
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.stalled {
		t.Fatal("fresh entry did not recover original target")
	}
}
