package swd

import (
	"context"
	"errors"
	"fmt"
)

func (c *Conn) readEntryIdentity(ctx context.Context) (uint32, error) {
	dpidr, err := c.readRaw(ctx, 0x00)
	// A joined error means the invalid-ACK data phase could not be completed.
	// Leave that transport failure to the caller instead of clocking activation.
	if err != ErrProtocol {
		return dpidr, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := c.enterThroughDormant(ctx); err != nil {
		return 0, errors.Join(ErrProtocol, fmt.Errorf("swd: dormant activation: %w", err))
	}
	return c.readRaw(ctx, 0x00)
}

func (c *Conn) enterThroughDormant(ctx context.Context) error {
	// IHI 0031H B5.3: nine high clocks and the 31-bit JTAG-to-DS code.
	entry := &sequence{}
	entry.appendN(9, true, true)
	for bit := range 31 {
		entry.append(true, uint32(0x33bbbbba)&(1<<uint(bit)) != 0)
	}
	if _, err := c.exchange(ctx, entry); err != nil {
		return err
	}
	// Send the 128-bit alert least-significant byte first. Keep each exchange
	// within the 136 clocks already required by JTAG-to-SWD entry.
	alert := &sequence{}
	alert.appendN(8, true, true)
	for _, value := range []byte{
		0x92, 0xf3, 0x09, 0x62, 0x95, 0x2d, 0x85, 0x86,
		0xe9, 0xaf, 0xdd, 0xe3, 0xa2, 0x0e, 0xbc, 0x19,
	} {
		alert.appendByte(true, value)
	}
	if _, err := c.exchange(ctx, alert); err != nil {
		return err
	}
	activation := &sequence{}
	activation.appendN(4, true, false)
	activation.appendByte(true, 0x1a)
	activation.appendN(56, true, true)
	activation.appendN(8, true, false)
	_, err := c.exchange(ctx, activation)
	return err
}
