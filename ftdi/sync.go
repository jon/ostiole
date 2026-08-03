package ftdi

import (
	"context"
	"fmt"
)

func (c *Channel) synchronize(ctx context.Context) error {
	const invalidCommand = 0xab
	if err := c.writeExact(ctx, []byte{invalidCommand}); err != nil {
		return err
	}
	payload, err := c.readPayload(ctx, 2)
	if err != nil {
		return err
	}
	if payload[0] != 0xfa || payload[1] != invalidCommand {
		return fmt.Errorf("ftdi: unexpected MPSSE synchronization %x", payload)
	}
	return nil
}
