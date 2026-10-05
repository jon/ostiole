package sim_test

import (
	"testing"

	"github.com/jon/ostiole/target/cortexm/sim"
)

func TestSharedClockStableTiesAndStaleCompletions(t *testing.T) {
	clock := new(sim.Clock)
	c, err := sim.New(sim.Config{Profile: sim.M33, Initial: sim.Snapshot{DHCSR: secure}, Clock: clock, HaltDelay: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	// Reset invalidates the pending automatic halt completion.
	if err := c.Schedule(3, sim.WarmReset); err != nil {
		t.Fatal(err)
	}
	if err := c.Schedule(5, sim.ExternalHalt); err != nil {
		t.Fatal(err)
	}
	if err := clock.Advance(5); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DFSR != 16 || c.Snapshot().DHCSR&halted == 0 {
		t.Fatalf("stale halt completed: %+v", c.Snapshot())
	}
	peer, err := sim.New(sim.Config{Profile: sim.M0, Clock: clock, Initial: sim.Snapshot{DHCSR: enable}})
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Schedule(6, sim.Retirement); err != nil {
		t.Fatal(err)
	}
	if err := peer.Schedule(6, sim.WarmReset); err != nil {
		t.Fatal(err)
	}
	if err := clock.Advance(1); err != nil {
		t.Fatal(err)
	}
	if peer.Snapshot().DHCSR&(1<<24|1<<25) != 1<<25 {
		t.Fatal("equal-time events reordered")
	}
}

func TestPermissionLatchesWhileHalted(t *testing.T) {
	c := newCore(t, sim.M33, sim.Snapshot{DHCSR: secure})
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	if err := c.Schedule(0, sim.RevokeSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := c.Clock().Advance(0); err != nil {
		t.Fatal(err)
	}
	if read(t, c, dhcsr)&secure == 0 {
		t.Fatal("permission changed while halted")
	}
	if err := write(t, c, dhcsr, key|enable); err != nil {
		t.Fatal(err)
	}
	if read(t, c, dhcsr)&secure != 0 {
		t.Fatal("permission did not update outside Debug state")
	}
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted != 0 {
		t.Fatal("halted without permission")
	}
}

func TestLatchedPermissionKeepsUnchangedHaltRequest(t *testing.T) {
	c := newCore(t, sim.M33, sim.Snapshot{DHCSR: secure | 3 | halted})
	if err := c.Schedule(0, sim.RevokeSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := c.Clock().Advance(0); err != nil {
		t.Fatal(err)
	}
	before := c.Snapshot()
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot() != before {
		t.Fatalf("unchanged halt request resumed core: %+v", c.Snapshot())
	}
}

func TestPermissionReturnCompletesRetainedHalt(t *testing.T) {
	c, err := sim.New(sim.Config{Profile: sim.M33, Initial: sim.Snapshot{DHCSR: secure}, HaltDelay: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	if err := c.Schedule(3, sim.RevokeSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := c.Schedule(6, sim.GrantSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := c.Clock().Advance(5); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted != 0 {
		t.Fatal("halted while denied")
	}
	if err := c.Clock().Advance(1); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&(secure|halted|halt) != secure|halted|halt {
		t.Fatalf("retained request lost: %+v", c.Snapshot())
	}
}

func TestEventValidationHasNoEffects(t *testing.T) {
	c := newCore(t, sim.M0, sim.Snapshot{})
	if err := c.Clock().Advance(100); err != nil {
		t.Fatal(err)
	}
	for _, event := range []sim.Event{0, sim.Event(99), sim.RevokeSecureDebug} {
		if err := c.Schedule(100, event); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
	if err := c.Schedule(99, sim.ExternalHalt); err == nil {
		t.Fatal("past event accepted")
	}

}

func TestExternalHaltSetsRequestOnlyOutsideDebugState(t *testing.T) {
	for _, profile := range []sim.Profile{sim.M0, sim.M33} {
		initial := uint32(enable)
		if profile == sim.M33 {
			initial |= secure
		}
		c := newCore(t, profile, sim.Snapshot{DHCSR: initial})
		if err := c.Schedule(0, sim.ExternalHalt); err != nil {
			t.Fatal(err)
		}
		if err := c.Clock().Advance(0); err != nil {
			t.Fatal(err)
		}
		if c.Snapshot().DHCSR&(halt|halted) != halt|halted || c.Snapshot().DFSR != 16 {
			t.Fatalf("external entry: %+v", c.Snapshot())
		}
		if err := write(t, c, dfsr, 16); err != nil {
			t.Fatal(err)
		}
		before := c.Snapshot()
		if err := c.Schedule(0, sim.ExternalHalt); err != nil {
			t.Fatal(err)
		}
		if err := c.Clock().Advance(0); err != nil {
			t.Fatal(err)
		}
		if c.Snapshot() != before {
			t.Fatalf("external request changed Debug state: %+v", c.Snapshot())
		}
	}
}

func TestExternalHaltDuringPendingResumeIsIgnored(t *testing.T) {
	for _, profile := range []sim.Profile{sim.M0, sim.M33} {
		initial := uint32(enable | halt | halted)
		if profile == sim.M33 {
			initial |= secure
		}
		c, err := sim.New(sim.Config{Profile: profile, Initial: sim.Snapshot{DHCSR: initial}, ResumeDelay: 5})
		if err != nil {
			t.Fatal(err)
		}
		if err := write(t, c, dhcsr, key|enable); err != nil {
			t.Fatal(err)
		}
		before := c.Snapshot()
		if err := c.Schedule(3, sim.ExternalHalt); err != nil {
			t.Fatal(err)
		}
		if err := c.Clock().Advance(3); err != nil {
			t.Fatal(err)
		}
		if c.Snapshot() != before {
			t.Fatalf("external request altered pending resume: %+v", c.Snapshot())
		}
		if err := c.Clock().Advance(2); err != nil {
			t.Fatal(err)
		}
		if c.Snapshot().DHCSR&(halt|halted) != 0 || c.Snapshot().DFSR != 0 {
			t.Fatalf("resume did not complete: %+v", c.Snapshot())
		}
	}
}

func TestExternalRestartDuringPendingHaltIsIgnored(t *testing.T) {
	for _, profile := range []sim.Profile{sim.M0, sim.M33} {
		initial := uint32(0)
		if profile == sim.M33 {
			initial |= secure
		}
		c, err := sim.New(sim.Config{Profile: profile, Initial: sim.Snapshot{DHCSR: initial}, HaltDelay: 5})
		if err != nil {
			t.Fatal(err)
		}
		if err := write(t, c, dhcsr, key|enable|halt); err != nil {
			t.Fatal(err)
		}
		before := c.Snapshot()
		if err := c.Schedule(3, sim.ExternalRestart); err != nil {
			t.Fatal(err)
		}
		if err := c.Clock().Advance(3); err != nil {
			t.Fatal(err)
		}
		if c.Snapshot() != before {
			t.Fatalf("external restart altered pending halt: %+v", c.Snapshot())
		}
		if err := c.Clock().Advance(2); err != nil {
			t.Fatal(err)
		}
		if c.Snapshot().DHCSR&(halt|halted) != halt|halted || c.Snapshot().DFSR != 1 {
			t.Fatalf("halt did not complete: %+v", c.Snapshot())
		}
	}
}
