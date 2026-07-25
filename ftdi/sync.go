package ftdi

import (
	"context"
	"fmt"
)

// Synchronize verifies that the MPSSE command parser is aligned.
func (c *Channel) Synchronize(ctx context.Context) error {
	const invalidCommand = 0xab
	if err := c.WriteExact(ctx, []byte{invalidCommand}); err != nil {
		return err
	}
	payload, err := c.ReadPayload(ctx, 2)
	if err != nil {
		return err
	}
	if payload[0] != 0xfa || payload[1] != invalidCommand {
		return fmt.Errorf(
			"ftdi: unexpected MPSSE synchronization %x",
			payload,
		)
	}
	return nil
}
