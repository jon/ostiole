package ftdi

import (
	"context"
	"errors"
	"fmt"
	"time"
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
	if err := c.claim(); err != nil {
		return fmt.Errorf("ftdi: claim USB interface: %w", err)
	}
	c.claimed = true
	for _, step := range []controlStep{
		{request: requestReset, value: 0},
		{request: requestReset, value: 1},
		{request: requestReset, value: 2},
		{request: requestSetLatency, value: 2},
		{request: requestSetBitmode, value: 0},
		{request: requestSetBitmode, value: 0x0200},
	} {
		if err := c.runControl(ctx, step); err != nil {
			return err
		}
	}
	return c.settle(ctx)
}

func (c *Channel) runControl(
	ctx context.Context,
	step controlStep,
) error {
	if _, err := c.control(ctx, step.request, step.value); err != nil {
		return fmt.Errorf(
			"ftdi: request %#02x value %#04x: %w",
			step.request,
			step.value,
			err,
		)
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

// Close restores the selected port, releases it, and closes the USB device.
func (c *Channel) Close() error {
	if c == nil || c.device == nil {
		return nil
	}
	var result error
	if c.claimed {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for _, step := range []controlStep{
			{request: requestSetBitmode, value: 0},
			{request: requestSetLatency, value: 16},
			{request: requestReset, value: 1},
			{request: requestReset, value: 2},
		} {
			result = errors.Join(result, c.runControl(ctx, step))
		}
		result = errors.Join(result, c.release())
		c.claimed = false
	}
	return errors.Join(result, c.device.Close())
}
