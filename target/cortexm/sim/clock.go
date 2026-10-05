package sim

import (
	"errors"
	"math"
	"sort"
)

// NoCompletion disables automatic completion of a configured halt or resume.
const NoCompletion = uint64(math.MaxUint64)

// Clock is an explicit virtual clock shared by modeled cores. Its zero value
// starts at tick zero. Do not copy an active Clock. Serialize advancement with every attached core and wire.
// Ticks have no physical duration and do not replace host context deadlines.
type Clock struct {
	now    uint64
	events []scheduled
}

type scheduled struct {
	at    uint64
	apply func()
}

// Now returns the current virtual tick. A nil clock reports zero.
func (c *Clock) Now() uint64 {
	if c == nil {
		return 0
	}
	return c.now
}

// Advance advances ticks and executes due events in time order, preserving
// insertion order at equal ticks. Advance(0) runs events queued for Now.
// Overflow fails without changing time or applying events.
func (c *Clock) Advance(ticks uint64) error {
	if c == nil {
		return errors.New("cortexm/sim: nil clock")
	}
	if ticks > math.MaxUint64-c.now {
		return errors.New("cortexm/sim: clock overflow")
	}
	end := c.now + ticks
	for len(c.events) > 0 && c.events[0].at <= end {
		e := c.events[0]
		c.events[0] = scheduled{}
		c.events = c.events[1:]
		c.now = e.at
		e.apply()
	}
	c.now = end
	return nil
}

func (c *Clock) enqueue(e scheduled) {
	c.events = append(c.events, e)
	sort.SliceStable(c.events, func(i, j int) bool { return c.events[i].at < c.events[j].at })
}

// Clock returns the core's clock, or nil for an invalid core.
func (c *Core) Clock() *Clock {
	if c == nil {
		return nil
	}
	return c.clock
}

func (c *Core) transitionDelay(control uint32) uint64 {
	if control&haltRequest != 0 {
		return c.haltDelay
	}
	return c.resumeDelay
}

func (c *Core) transitionPossible(control uint32) bool {
	return control&enabled != 0 && (control&haltRequest != 0) != (c.state.DHCSR&inDebug != 0)
}

func (c *Core) queueTransition(control uint32) {
	if !c.transitionPossible(control) {
		return
	}
	delay := c.transitionDelay(control)
	if delay == NoCompletion {
		return
	}
	generation := c.generation
	halt := control&haltRequest != 0
	c.clock.enqueue(scheduled{c.clock.now + delay, func() { c.completeTransition(generation, halt) }})
	if delay == 0 {
		_ = c.clock.Advance(0)
	}
}

func (c *Core) completeTransition(generation uint64, halt bool) {
	if generation != c.generation {
		return
	}
	if halt {
		if c.debugAllowed() && c.state.DHCSR&(enabled|haltRequest) == enabled|haltRequest {
			c.state.DHCSR |= inDebug | registerReady
			c.state.DFSR |= 1
		}
	} else if c.state.DHCSR&haltRequest == 0 {
		c.completeResume()
	}
}
