package usb

import "errors"

// ClaimedInterface owns one interface claim on a Device. Its methods must be
// serialized with calls on the device.
type ClaimedInterface struct {
	device *Device
	number uint8
}

// SetAltSetting selects an alternate setting on the claimed interface.
func (c *ClaimedInterface) SetAltSetting(alternate uint8) error {
	if c == nil || c.device == nil {
		return errors.New("usb: claimed interface is closed")
	}
	return c.device.setAltSetting(c, alternate)
}

// Close releases the interface. A failed release retains the claim so Close
// can be retried.
func (c *ClaimedInterface) Close() error {
	if c == nil || c.device == nil {
		return nil
	}
	return c.device.releaseInterface(c)
}
