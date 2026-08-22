package swd

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	connectWaitLimit = 100

	overrunDetect = uint32(1 << 0)
	stickyOverrun = uint32(1 << 1)
	writeDataErr  = uint32(1 << 7)

	clearStickyCompare  = uint32(1 << 1)
	clearStickyError    = uint32(1 << 2)
	clearWriteDataError = uint32(1 << 3)
	clearStickyOverrun  = uint32(1 << 4)
)

// Connect enters SWD, reads and validates DPIDR before configuration, clears
// supported sticky state, selects DP bank zero, and establishes the target's
// response grammar. It keeps an inherited ORUNDETECT setting or tries to enable
// it, and uses overrun framing only if the bit reads back as set. Release later
// restores the setting found during bootstrap but does not restore SELECT or
// cleared sticky state. Connect cleans up a failed attempt when possible; a
// joined cleanup error leaves Release available for retry. Calling Connect
// again repairs framing and rejects a changed DPIDR.
func (c *Conn) Connect(ctx context.Context) (dpidr uint32, err error) {
	if c == nil || c.wire == nil {
		return 0, errors.New("swd: nil connection")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	defer c.cleanupFailedConnect(&err)
	dpidr, state, err := c.prepareConnection(ctx)
	if err != nil {
		return 0, err
	}
	inherited := state&overrunDetect != 0
	if !c.originalKnown {
		c.originalOverrun = inherited
		c.originalKnown = true
	}
	if inherited {
		c.response = responseOverrun
	} else if _, err := c.changeOverrun(ctx, state, true, true); err != nil {
		return 0, fmt.Errorf("swd: enable overrun detection: %w", err)
	}
	c.state = connectionReady
	return dpidr, nil
}

func (c *Conn) prepareConnection(ctx context.Context) (uint32, uint32, error) {
	c.state = connectionRepair
	c.response = responseSimple
	c.invalidateBank()
	if err := c.enterSWD(ctx); err != nil {
		return 0, 0, fmt.Errorf("swd: enter protocol: %w", err)
	}
	dpidr, err := c.bootstrap(ctx)
	if err != nil {
		return 0, 0, err
	}
	c.identity = dpidr
	c.identityKnown = true
	state, err := c.readRaw(ctx, 0x04)
	if err != nil {
		c.requireRepair()
		return 0, 0, fmt.Errorf("swd: read CTRL/STAT: %w", err)
	}
	return dpidr, state, nil
}

func (c *Conn) cleanupFailedConnect(connectErr *error) {
	if *connectErr == nil {
		return
	}
	if !c.originalKnown {
		c.identity = 0
		c.identityKnown = false
	}
	if c.state == connectionIdle {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Release(cleanupCtx); err != nil {
		*connectErr = errors.Join(*connectErr, fmt.Errorf("swd: clean up failed Connect: %w", err))
	}
}

// Release restores the ORUNDETECT setting found by Connect. A failed release
// retains enough state for another call to retry. Release is harmless on an
// idle or nil connection.
func (c *Conn) Release(ctx context.Context) error {
	if c == nil || c.wire == nil || c.state == connectionIdle {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.prepareRelease(ctx); err != nil {
		return err
	}
	if err := c.restoreOverrun(ctx); err != nil {
		return err
	}
	c.finishRelease()
	return nil
}

func (c *Conn) prepareRelease(ctx context.Context) error {
	if c.state != connectionReady || !c.bank.valid || c.selectPending {
		if err := c.reenterForRelease(ctx); err != nil {
			return err
		}
	}
	if c.bank.bank == 0 {
		return nil
	}
	if err := c.writeRaw(ctx, 0x08, 0); err != nil {
		c.requireRepair()
		return fmt.Errorf("swd: select DP bank zero for release: %w", err)
	}
	if _, err := c.readRaw(ctx, 0x0c); err != nil {
		c.requireRepair()
		return fmt.Errorf("swd: settle DP bank zero for release: %w", err)
	}
	c.bank = bankSelection{valid: true}
	return nil
}

func (c *Conn) reenterForRelease(ctx context.Context) error {
	c.response = responseSimple
	c.invalidateBank()
	if err := c.enterSWD(ctx); err != nil {
		c.requireRepair()
		return fmt.Errorf("swd: re-enter protocol for release: %w", err)
	}
	if _, err := c.bootstrap(ctx); err != nil {
		return fmt.Errorf("swd: bootstrap release: %w", err)
	}
	state, err := c.readRaw(ctx, 0x04)
	if err != nil {
		c.requireRepair()
		return fmt.Errorf("swd: read CTRL/STAT for release: %w", err)
	}
	if state&overrunDetect != 0 {
		c.response = responseOverrun
	}
	return nil
}

func (c *Conn) restoreOverrun(ctx context.Context) error {
	state, err := c.readRaw(ctx, 0x04)
	if err != nil {
		c.requireRepair()
		return fmt.Errorf("swd: read CTRL/STAT for release: %w", err)
	}
	want := state&overrunDetect != 0
	if c.originalKnown {
		want = c.originalOverrun
	}
	if (state&overrunDetect != 0) == want {
		return nil
	}
	if _, err := c.changeOverrun(ctx, state, want, false); err != nil {
		return fmt.Errorf("swd: restore overrun detection: %w", err)
	}
	return nil
}

func (c *Conn) finishRelease() {
	c.state = connectionIdle
	c.response = responseSimple
	c.identity = 0
	c.identityKnown = false
	c.originalOverrun = false
	c.originalKnown = false
	c.invalidateBank()
}

func (c *Conn) bootstrap(ctx context.Context) (uint32, error) {
	dpidr, err := c.readRaw(ctx, 0x00)
	if err != nil {
		c.requireRepair()
		return 0, fmt.Errorf("swd: read DPIDR: %w", err)
	}
	if dpidr&1 == 0 {
		if c.identityKnown {
			c.requireRepair()
		} else {
			c.state = connectionIdle
			c.response = responseSimple
			c.invalidateBank()
		}
		return 0, fmt.Errorf("swd: invalid DPIDR %#08x: constant bit is clear", dpidr)
	}
	if c.identityKnown && dpidr != c.identity {
		c.requireRepair()
		return 0, fmt.Errorf("swd: DPIDR changed from %#08x to %#08x", c.identity, dpidr)
	}
	clear := clearStickyError | clearWriteDataError | clearStickyOverrun
	if dpidr>>16&1 == 0 {
		clear |= clearStickyCompare
	}
	if err := c.writeRaw(ctx, 0x00, clear); err != nil {
		c.requireRepair()
		return 0, fmt.Errorf("swd: clear sticky state: %w", err)
	}
	if err := c.writeRaw(ctx, 0x08, 0); err != nil {
		c.requireRepair()
		return 0, fmt.Errorf("swd: select DP bank zero: %w", err)
	}
	if _, err := c.readRaw(ctx, 0x0c); err != nil {
		c.requireRepair()
		return 0, fmt.Errorf("swd: settle DP bank zero: %w", err)
	}
	c.bank = bankSelection{valid: true}
	return dpidr, nil
}

func (c *Conn) changeOverrun(ctx context.Context, state uint32, enabled, allowFallback bool) (bool, error) {
	for attempts := 0; attempts <= connectWaitLimit; attempts++ {
		var err error
		state, err = c.prepareOverrunWrite(ctx, state)
		if err != nil {
			return false, err
		}
		value := state
		if enabled {
			value |= overrunDetect
		} else {
			value &^= overrunDetect
		}
		if err := c.writeRaw(ctx, 0x04, value); err != nil {
			c.requireRepair()
			return false, err
		}
		applied, retry, observed, err := c.settleOverrunWrite(ctx, enabled, allowFallback)
		if err != nil || !retry {
			return applied, err
		}
		state = observed
	}
	c.requireRepair()
	return false, errors.New("ORUNDETECT transition retry limit exceeded")
}

func (c *Conn) prepareOverrunWrite(ctx context.Context, state uint32) (uint32, error) {
	if c.response != responseOverrun || state&stickyOverrun == 0 {
		return state, nil
	}
	if err := c.writeRaw(ctx, 0x00, clearStickyOverrun); err != nil {
		c.requireRepair()
		return 0, err
	}
	return state &^ stickyOverrun, nil
}

func (c *Conn) settleOverrunWrite(ctx context.Context, enabled, allowFallback bool) (bool, bool, uint32, error) {
	for attempts := 0; attempts <= connectWaitLimit; attempts++ {
		_, err := c.readRaw(ctx, 0x0c)
		if err == ErrWait && c.response == responseSimple {
			continue
		}
		return c.resolveOverrunBarrier(ctx, err, enabled, allowFallback)
	}
	c.requireRepair()
	return false, false, 0, errors.New("ORUNDETECT barrier retry limit exceeded")
}

func (c *Conn) resolveOverrunBarrier(ctx context.Context, barrierErr error, enabled, allowFallback bool) (bool, bool, uint32, error) {
	switch barrierErr {
	case nil, ErrParity:
		applied, err := c.verifyOverrunChange(ctx, enabled, allowFallback)
		return applied, false, 0, err
	case ErrWait:
		state, err := c.recoverAbandonedOverrunWrite(ctx)
		return false, true, state, err
	case ErrFault:
		return c.resolveOverrunFault(ctx, enabled, allowFallback)
	default:
		c.requireRepair()
		return false, false, 0, barrierErr
	}
}

func (c *Conn) verifyOverrunChange(ctx context.Context, enabled, allowFallback bool) (bool, error) {
	c.setResponse(enabled)
	state, err := c.readRaw(ctx, 0x04)
	if err != nil {
		c.requireRepair()
		return false, err
	}
	applied := state&overrunDetect != 0 == enabled
	if applied {
		return true, nil
	}
	c.response = responseSimple
	if allowFallback {
		return false, nil
	}
	c.requireRepair()
	return false, errors.New("CTRL/STAT.ORUNDETECT did not change")
}

func (c *Conn) recoverAbandonedOverrunWrite(ctx context.Context) (uint32, error) {
	state, err := c.readRaw(ctx, 0x04)
	if err != nil {
		c.requireRepair()
		return 0, err
	}
	if state&writeDataErr == 0 {
		return state, nil
	}
	if err := c.writeRaw(ctx, 0x00, clearWriteDataError); err != nil {
		c.requireRepair()
		return 0, err
	}
	return state &^ writeDataErr, nil
}

func (c *Conn) resolveOverrunFault(ctx context.Context, enabled, allowFallback bool) (bool, bool, uint32, error) {
	state, err := c.readRaw(ctx, 0x04)
	if err != nil {
		c.requireRepair()
		return false, false, 0, errors.Join(ErrFault, err)
	}
	c.setResponse(state&overrunDetect != 0)
	if err := c.clearOverrunTransitionFault(ctx, state); err != nil {
		return false, false, 0, errors.Join(ErrFault, err)
	}
	applied := state&overrunDetect != 0 == enabled
	if applied || allowFallback {
		return applied, false, 0, nil
	}
	return false, true, state &^ (writeDataErr | stickyOverrun), nil
}

func (c *Conn) clearOverrunTransitionFault(ctx context.Context, state uint32) error {
	var clear uint32
	if state&writeDataErr != 0 {
		clear |= clearWriteDataError
	}
	if state&stickyOverrun != 0 {
		clear |= clearStickyOverrun
	}
	if clear == 0 {
		return nil
	}
	if err := c.writeRaw(ctx, 0x00, clear); err != nil {
		c.requireRepair()
		return err
	}
	return nil
}

func (c *Conn) setResponse(overrun bool) {
	c.response = responseSimple
	if overrun {
		c.response = responseOverrun
	}
}

func (c *Conn) readRaw(ctx context.Context, addr uint8) (uint32, error) {
	req, err := newRequest(false, true, addr)
	if err != nil {
		return 0, err
	}
	return c.execute(ctx, req, 0)
}

func (c *Conn) writeRaw(ctx context.Context, addr uint8, value uint32) error {
	req, err := newRequest(false, false, addr)
	if err != nil {
		return err
	}
	_, err = c.execute(ctx, req, value)
	return err
}
