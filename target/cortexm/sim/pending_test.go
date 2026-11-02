package sim_test

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/jon/ostiole/target/cortexm/sim"
)

func expectHalt(t *testing.T, core *sim.Core, want bool) {
	t.Helper()
	if got := core.Snapshot().DHCSR&halted != 0; got != want {
		t.Fatalf("tick%d halt=%v want%v state=%+v", core.Clock().Now(), got, want, core.Snapshot())
	}
}

func pendingTransition(t *testing.T, clock *sim.Clock, resume bool, delay uint64) *sim.Core {
	t.Helper()
	initial := uint32(enable)
	request := uint32(enable | halt)
	if resume {
		initial, request = enable|halt|halted, enable
	}
	core, err := sim.New(sim.Config{Profile: sim.M0, Clock: clock, Initial: sim.Snapshot{DHCSR: initial}, HaltDelay: delay, ResumeDelay: delay})
	if err != nil {
		t.Fatal(err)
	}
	if err := write(t, core, dhcsr, key|request); err != nil {
		t.Fatal(err)
	}
	return core
}

func TestInheritedPendingHaltUsesConfiguredDelay(t *testing.T) {
	for _, profile := range []sim.Profile{sim.M0, sim.M33} {
		for _, delay := range []uint64{0, 3, sim.NoCompletion} {
			t.Run(fmt.Sprintf("profile%d/delay%d", profile, delay), func(t *testing.T) {
				initial := uint32(enable | halt)
				if profile == sim.M33 {
					initial |= secure
				}
				core, err := sim.New(sim.Config{Profile: profile, Initial: sim.Snapshot{DHCSR: initial}, HaltDelay: delay})
				if err != nil {
					t.Fatal(err)
				}
				expectHalt(t, core, delay == 0)
				switch delay {
				case 3:
					if err := core.Clock().Advance(2); err != nil {
						t.Fatal(err)
					}
					expectHalt(t, core, false)
					if err := core.Clock().Advance(1); err != nil {
						t.Fatal(err)
					}
					expectHalt(t, core, true)
				case sim.NoCompletion:
					if err := core.Clock().Advance(100); err != nil {
						t.Fatal(err)
					}
					expectHalt(t, core, false)
				}
			})
		}
	}
}

func TestDeniedRepeatedHaltRetainsRequestAndReadyCompletion(t *testing.T) {
	core, err := sim.New(sim.Config{Profile: sim.M33, Initial: sim.Snapshot{DHCSR: secure}, HaltDelay: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := write(t, core, dhcsr, key|enable|halt); err != nil {
		t.Fatal(err)
	}
	if err := core.Schedule(3, sim.RevokeSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := core.Schedule(7, sim.GrantSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := core.Clock().Advance(3); err != nil {
		t.Fatal(err)
	}
	for _, ticks := range []uint64{2, 2} {
		if err := write(t, core, dhcsr, key|enable|halt); err != nil {
			t.Fatal(err)
		}
		if core.Snapshot().DHCSR&halt == 0 {
			t.Fatal("denied repeat cleared a retained halt request")
		}
		expectHalt(t, core, false)
		if err := core.Clock().Advance(ticks); err != nil {
			t.Fatal(err)
		}
	}
	expectHalt(t, core, true)
	if core.Snapshot().DFSR != 1 {
		t.Fatalf("completed halt reason=%#x", core.Snapshot().DFSR)
	}
}

func TestDeniedPendingHaltCanStillBeCanceled(t *testing.T) {
	core, err := sim.New(sim.Config{Profile: sim.M33, Initial: sim.Snapshot{DHCSR: enable | halt}, HaltDelay: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Clock().Advance(3); err != nil {
		t.Fatal(err)
	}
	expectHalt(t, core, false)
	if err := write(t, core, dhcsr, key|enable); err != nil {
		t.Fatal(err)
	}
	if err := core.Schedule(4, sim.GrantSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := core.Clock().Advance(1); err != nil {
		t.Fatal(err)
	}
	if core.Snapshot().DHCSR&halt != 0 {
		t.Fatal("explicit cancellation retained halt request")
	}
	expectHalt(t, core, false)
}

func TestInheritedDeniedHaltCompletesWhenPermissionReturns(t *testing.T) {
	core, err := sim.New(sim.Config{Profile: sim.M33, Initial: sim.Snapshot{DHCSR: enable | halt}, HaltDelay: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Schedule(4, sim.GrantSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := core.Clock().Advance(3); err != nil {
		t.Fatal(err)
	}
	expectHalt(t, core, false)
	if err := core.Clock().Advance(1); err != nil {
		t.Fatal(err)
	}
	expectHalt(t, core, true)
}

func TestUnchangedTransitionKeepsOriginalTick(t *testing.T) {
	for _, resume := range []bool{false, true} {
		core := pendingTransition(t, new(sim.Clock), resume, 5)
		if err := core.Clock().Advance(3); err != nil {
			t.Fatal(err)
		}
		request := uint32(enable | halt)
		if resume {
			request = enable
		}
		if err := write(t, core, dhcsr, key|request); err != nil {
			t.Fatal(err)
		}
		if err := core.Clock().Advance(2); err != nil {
			t.Fatal(err)
		}
		expectHalt(t, core, !resume)
	}
}

func TestUnchangedTransitionDoesNotCheckNewDeadline(t *testing.T) {
	for _, resume := range []bool{false, true} {
		clock := new(sim.Clock)
		if err := clock.Advance(math.MaxUint64 - 10); err != nil {
			t.Fatal(err)
		}
		core := pendingTransition(t, clock, resume, 8)
		if err := clock.Advance(5); err != nil {
			t.Fatal(err)
		}
		request := uint32(enable | halt)
		if resume {
			request = enable
		}
		if err := write(t, core, dhcsr, key|request); err != nil {
			t.Fatalf("unchanged request rejected near clock end: %v", err)
		}
		if err := clock.Advance(3); err != nil {
			t.Fatal(err)
		}
		expectHalt(t, core, !resume)
	}
}

func TestCanceledAndReissuedTransitionRejectsStaleCompletion(t *testing.T) {
	for _, resume := range []bool{false, true} {
		core := pendingTransition(t, new(sim.Clock), resume, 5)
		if err := core.Clock().Advance(2); err != nil {
			t.Fatal(err)
		}
		cancel, reissue := uint32(enable), uint32(enable|halt)
		if resume {
			cancel, reissue = reissue, cancel
		}
		if err := write(t, core, dhcsr, key|cancel); err != nil {
			t.Fatal(err)
		}
		if err := write(t, core, dhcsr, key|reissue); err != nil {
			t.Fatal(err)
		}
		if err := core.Clock().Advance(3); err != nil {
			t.Fatal(err)
		}
		expectHalt(t, core, resume)
		if err := core.Clock().Advance(2); err != nil {
			t.Fatal(err)
		}
		expectHalt(t, core, !resume)
	}
}

func TestInheritedPendingHaltRejectsOverflowWithoutEffects(t *testing.T) {
	clock := new(sim.Clock)
	if err := clock.Advance(math.MaxUint64 - 1); err != nil {
		t.Fatal(err)
	}
	core, err := sim.New(sim.Config{Profile: sim.M0, Initial: sim.Snapshot{DHCSR: enable | halt}, Clock: clock, HaltDelay: 2})
	if core != nil || !errors.Is(err, sim.ErrUnsupported) || clock.Now() != math.MaxUint64-1 {
		t.Fatalf("overflowing initial halt: core=%v error=%v tick%d", core, err, clock.Now())
	}
	core, err = sim.New(sim.Config{Profile: sim.M0, Initial: sim.Snapshot{DHCSR: enable | halt}, Clock: clock, HaltDelay: sim.NoCompletion})
	if err != nil {
		t.Fatal(err)
	}
	if err := clock.Advance(1); err != nil {
		t.Fatal(err)
	}
	expectHalt(t, core, false)
}
