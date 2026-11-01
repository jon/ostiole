package ftdi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

const (
	requestReset      = 0x00
	requestSetLatency = 0x09
	requestSetBitmode = 0x0b
)

type controlStep struct {
	request uint8
	value   uint16
}

func (c *Channel) enterMPSSE(ctx context.Context) error {
	if err := c.claimUSB(); err != nil {
		return fmt.Errorf("ftdi: claim USB interface: %w", err)
	}
	for _, step := range []controlStep{
		{request: requestReset, value: 0},
		{request: requestSetBitmode, value: 0},
		{request: requestReset, value: 1},
		{request: requestReset, value: 2},
		{request: requestSetLatency, value: 2},
		{request: requestSetBitmode, value: 0x0200},
	} {
		if err := c.runControl(ctx, step); err != nil {
			return err
		}
	}
	return c.settle(ctx)
}

func (c *Channel) runControl(ctx context.Context, step controlStep) error {
	if _, err := c.control(ctx, step.request, step.value); err != nil {
		return fmt.Errorf("ftdi: request %#02x value %#04x: %w", step.request, step.value, err)
	}
	return nil
}

func settleMPSSE(ctx context.Context) error {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Close drains pending bulk OUT, resets bit mode, sets latency to 16 ms,
// purges both paths, and releases the interface and device. It does not restore
// prior FTDI settings. Live restoration failures retain ownership for retry.
// Device loss or a poisoned stream abandons restoration and continues host
// cleanup. Final errors are returned on that attempt; later calls return nil
// after release. Freeing host resources does not restore a halted target.
func (c *Channel) Close() error {
	if c == nil || c.device == nil {
		return nil
	}
	c.active = false
	result := c.restoreForClose()
	if result != nil && !c.restored {
		return result
	}
	if err := c.releaseUSB(); err != nil {
		result = errors.Join(result, err)
		if c.claim != nil {
			return result
		}
	}
	err := c.device.Close()
	result = errors.Join(result, err)
	if err == nil || !cleanupPending(c.device) {
		c.device = nil
	}
	return result
}

func (c *Channel) restoreForClose() error {
	if c.restored || c.claim == nil {
		return nil
	}
	err := c.claim.AbortBulk(c.bulkOut)
	c.recordLoss(err)
	if reason := c.restorationFailure(); reason != nil {
		c.restored = true
		return errors.Join(err, fmt.Errorf("ftdi: abandon adapter restoration: %w", reason))
	}
	if err != nil {
		return fmt.Errorf("ftdi: drain bulk OUT before close: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, step := range []controlStep{
		{request: requestSetBitmode, value: 0},
		{request: requestSetLatency, value: 16},
		{request: requestReset, value: 1},
		{request: requestReset, value: 2},
	} {
		if err := c.runControl(ctx, step); err != nil {
			if reason := c.restorationFailure(); reason != nil {
				c.restored = true
				return errors.Join(err, fmt.Errorf("ftdi: abandon adapter restoration: %w", reason))
			}
			return err
		}
	}
	c.restored = true
	return nil
}

// RestorationPossible reports whether the channel can communicate for
// restoration. Device loss, a poisoned stream, and a closed channel report
// false. The query performs no I/O and does not confirm restored state.
func (c *Channel) RestorationPossible() bool {
	return c != nil && c.device != nil && c.restorationFailure() == nil
}

// CleanupPending reports whether adapter restoration or host cleanup remains,
// without I/O. Unknown USB implementations remain conservative.
func (c *Channel) CleanupPending() bool {
	return c != nil && ((!c.restored && c.claim != nil) ||
		(c.claim != nil && cleanupPending(c.claim)) || (c.device != nil && cleanupPending(c.device)))
}

func cleanupPending(owner interface{ Close() error }) bool {
	if state, ok := owner.(probe.CleanupState); ok {
		return state.CleanupPending()
	}
	return true
}

func (c *Channel) restorationFailure() error {
	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()
	var err error
	if c.gone {
		err = usb.ErrDeviceGone
	}
	if c.poisonErr != nil {
		err = errors.Join(err, c.poisonErr, ErrChannelPoisoned)
	}
	return err
}

func (c *Channel) recordLoss(err error) {
	if errors.Is(err, usb.ErrDeviceGone) {
		c.receiveMu.Lock()
		c.gone = true
		c.receiveMu.Unlock()
	}
}
