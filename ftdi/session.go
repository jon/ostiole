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

// Close resets bit mode, sets the latency timer to 16 ms, purges the receive
// and transmit paths, releases the selected port, and closes the USB device. It
// does not restore the function's prior settings. Pending bulk OUT work is
// drained before adapter state changes. If cancellation or interface release
// fails, the channel remains open and Close can be retried.
func (c *Channel) Close() error {
	if c == nil || c.device == nil {
		return nil
	}
	var result error
	if c.claim != nil {
		if err := c.claim.AbortBulk(c.bulkOut); err != nil {
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
			result = errors.Join(result, c.runControl(ctx, step))
		}
		if err := c.releaseUSB(); err != nil {
			return errors.Join(result, err)
		}
	}
	return errors.Join(result, c.device.Close())
}
