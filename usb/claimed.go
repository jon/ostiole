package usb

import "errors"

// ClaimedInterface owns one interface claim on a Device. Its methods must be
// serialized with calls on the device.
type ClaimedInterface struct {
	device    *Device
	number    uint8
	alternate uint8
	altKnown  bool
	endpoints map[uint8]Endpoint
	transfers bulkTransferEngine
}

// SetAltSetting selects an alternate setting on the claimed interface. If the
// host reports a failed selection, the next endpoint lookup reads the active
// alternate again.
func (c *ClaimedInterface) SetAltSetting(alternate uint8) error {
	if c == nil || c.device == nil {
		return errors.New("usb: claimed interface is closed")
	}
	if c.transfers != nil {
		if c.transfers.pending() {
			return errors.New("usb: cannot select an alternate setting while bulk transfers are pending")
		}
		if err := c.transfers.close(); err != nil {
			return err
		}
		c.transfers = nil
	}
	c.altKnown = false
	c.endpoints = nil
	if err := c.device.setAltSetting(c, alternate); err != nil {
		return err
	}
	c.alternate = alternate
	c.altKnown = true
	return nil
}

// Close releases the interface. A failed release retains the claim so Close
// can be retried.
func (c *ClaimedInterface) Close() error {
	if c == nil || c.device == nil {
		return nil
	}
	if c.transfers != nil {
		if err := c.transfers.close(); err != nil {
			return err
		}
		c.transfers = nil
	}
	return c.device.releaseInterface(c)
}
