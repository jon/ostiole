package ftdi

import (
	"context"
	"fmt"
)

const invalidCommand = 0xab

func (c *Channel) synchronize(ctx context.Context) error {
	payload, err := c.exchangePayload(ctx, []byte{invalidCommand, cmdSendImmediate}, 2)
	if err != nil {
		return err
	}
	if payload[0] != 0xfa || payload[1] != invalidCommand {
		return fmt.Errorf("ftdi: unexpected MPSSE synchronization %x", payload)
	}
	return nil
}
