package sim_test

import (
	"github.com/jon/ostiole/target/cortexm/sim"
	"math"
	"testing"
)

func TestExplicitClockSeparatesRequestCompletionAndObservation(t *testing.T) {
	clock := new(sim.Clock)
	c, err := sim.New(sim.Config{Profile: sim.M0, Clock: clock, HaltDelay: 3, ResumeDelay: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if read(t, c, dhcsr)&halted != 0 {
			t.Fatal("status read completed pending halt")
		}
	}
	if c.Clock() != clock || clock.Now() != 0 {
		t.Fatal("wrong clock")
	}
	if err := clock.Advance(2); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted != 0 {
		t.Fatal("halt early")
	}
	if err := clock.Advance(1); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted == 0 {
		t.Fatal("halt missing")
	}
	if err := write(t, c, dhcsr, key|enable); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted == 0 {
		t.Fatal("resume early")
	}
	if err := clock.Advance(2); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted != 0 {
		t.Fatal("resume missing")
	}
}

func TestClockValidationNeverCompletionAndOverflow(t *testing.T) {
	c, err := sim.New(sim.Config{Profile: sim.M0, HaltDelay: sim.NoCompletion})
	if err != nil {
		t.Fatal(err)
	}
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	if err := c.Clock().Advance(100); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted != 0 {
		t.Fatal("never halt completed")
	}
	if err := c.Clock().Advance(math.MaxUint64 - 100); err != nil {
		t.Fatal(err)
	}
	if err := c.Clock().Advance(1); err == nil || c.Clock().Now() != math.MaxUint64 {
		t.Fatal("clock wrapped")
	}
	c, err = sim.New(sim.Config{Profile: sim.M0, Clock: c.Clock(), HaltDelay: 1})
	if err != nil {
		t.Fatal(err)
	}
	before := c.Snapshot()
	if err := write(t, c, dhcsr, key|3); err == nil || c.Snapshot() != before {
		t.Fatal("overflowing completion accepted")
	}
}
