package sim

import "errors"

// Event selects a supported hardware event. Zero is invalid. Events neither
// execute instructions nor acquire or release a driver's halt ownership.
type Event uint8

const (
	// Retirement records retirement without computing instruction effects.
	Retirement Event = iota + 1
	// WarmReset resets execution/debug request state, preserving debug enable,
	// Secure permission and DFSR. It is a fixture event, not a reset API.
	WarmReset
	// ExternalRestart clears the halt request and leaves Debug state. Requests
	// outside Debug state are ignored. M33 records sticky restart on exit;
	// no instruction effects are synthesized.
	ExternalRestart
	// ExternalHalt requests entry to Debug state if halting debug is allowed.
	// The request is ignored while already in Debug state.
	ExternalHalt
	// GrantSecureDebug permits Secure invasive debug on M33.
	GrantSecureDebug
	// RevokeSecureDebug removes Secure invasive debug permission on M33.
	// Reported S_SDE remains latched until the core leaves Debug state.
	RevokeSecureDebug
)

// Schedule queues an event at an absolute virtual tick. Time must not precede
// Clock.Now. Permission events require M33. Scheduling does not apply events;
// advance the shared clock explicitly. Equal-time events keep insertion order.
func (c *Core) Schedule(at uint64, event Event) error {
	if c == nil || c.clock == nil {
		return errors.New("cortexm/sim: invalid core")
	}
	if event < Retirement || event > RevokeSecureDebug || at < c.clock.now {
		return errors.New("cortexm/sim: invalid event or past time")
	}
	if c.profile != M33 && (event == GrantSecureDebug || event == RevokeSecureDebug) {
		return ErrUnsupported
	}
	c.clock.enqueue(scheduled{at, func() { c.applyEvent(event) }})
	return nil
}

func (c *Core) applyEvent(event Event) {
	switch event {
	case Retirement:
		if c.state.DHCSR&inDebug == 0 {
			c.state.DHCSR |= retired
		}
	case WarmReset:
		c.invalidateTransition()
		c.state.DHCSR &^= haltRequest | maskInterrupts | inDebug | retired | restarted
		c.state.DHCSR |= reset
		c.updatePermission()
	case ExternalRestart:
		if c.state.DHCSR&inDebug != 0 {
			c.invalidateTransition()
			c.state.DHCSR &^= haltRequest
			c.completeResume()
		}
	case ExternalHalt:
		if c.debugAllowed() && c.state.DHCSR&(enabled|inDebug) == enabled {
			c.invalidateTransition()
			c.state.DHCSR |= haltRequest | inDebug | registerReady
			c.state.DFSR |= 16
		}
	case GrantSecureDebug, RevokeSecureDebug:
		c.secureAllowed = event == GrantSecureDebug
		c.updatePermission()
		c.completeReadyHalt()
	}
}

func (c *Core) debugAllowed() bool {
	return c.profile == M0 || c.state.DHCSR&secureDebug != 0
}

func (c *Core) updatePermission() {
	if c.profile != M33 || c.state.DHCSR&inDebug != 0 {
		return
	}
	c.state.DHCSR &^= secureDebug
	if c.secureAllowed {
		c.state.DHCSR |= secureDebug
	}
}
