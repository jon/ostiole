package sim

import "errors"

// ErrUnsupported reports behavior outside this model or an architecturally
// unpredictable request. It is a fixture/model failure, not a target bus fault.
var ErrUnsupported = errors.New("cortexm/sim: unsupported debug behavior")

func (c *Core) writeControl(value uint32) error {
	if value>>16 != 0xa05f {
		return nil
	}
	control := value & (enabled | haltRequest | step | maskInterrupts | snapStall)
	if err := c.checkControl(control); err != nil {
		return err
	}
	if control&enabled == 0 {
		c.state.DHCSR &^= enabled | haltRequest | maskInterrupts
		return nil
	}
	if !c.debugAllowed() {
		control &^= haltRequest
	}
	previous := c.state.DHCSR & (enabled | haltRequest)
	c.state.DHCSR = c.state.DHCSR & ^(enabled|haltRequest|maskInterrupts|snapStall) | control
	if previous != control&(enabled|haltRequest) {
		if control&haltRequest != 0 {
			c.state.DHCSR |= inDebug | registerReady
			c.state.DFSR |= 1
		} else {
			c.completeResume()
		}
	}
	return nil
}

func (c *Core) checkControl(control uint32) error {
	if c.unsafeMemory && control&haltRequest == 0 {
		return ErrUnsupported
	}
	if control&enabled == 0 {
		if c.state.DHCSR&inDebug != 0 {
			return ErrUnsupported
		}
		return nil
	}
	if control&(step|snapStall) != 0 {
		return ErrUnsupported
	}
	if c.state.DHCSR&enabled == 0 {
		if control&maskInterrupts != 0 {
			return ErrUnsupported
		}
		return nil
	}
	if (control^c.state.DHCSR)&maskInterrupts != 0 {
		if c.state.DHCSR&(enabled|haltRequest|inDebug) != enabled|haltRequest|inDebug || control&haltRequest == 0 {
			return ErrUnsupported
		}
	}
	return nil
}

func (c *Core) completeResume() {
	if c.profile == M33 && c.state.DHCSR&inDebug != 0 {
		c.state.DHCSR |= restarted
	}
	c.state.DHCSR &^= inDebug
}

func (c *Core) debugAllowed() bool {
	return c.profile == M0 || c.state.DHCSR&secureDebug != 0
}
