//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
)

var errDarwinUSBUnsupported = errors.New("usb: macOS host access is not implemented")

// ClaimInterface claims one interface for this device.
func (d *Device) ClaimInterface(uint8) error {
	return errDarwinUSBUnsupported
}

// SetAltSetting selects an alternate setting on the claimed interface.
func (d *Device) SetAltSetting(uint8, uint8) error {
	return errDarwinUSBUnsupported
}

// ReleaseInterface releases the claimed interface.
func (d *Device) ReleaseInterface(uint8) error {
	return errDarwinUSBUnsupported
}

// ControlTransfer performs one deadline-bounded endpoint-zero transfer.
func (d *Device) ControlTransfer(context.Context, uint8, uint8, uint16, uint16, []byte) (int, error) {
	return 0, errDarwinUSBUnsupported
}

// BulkWrite writes to one OUT endpoint.
func (d *Device) BulkWrite(context.Context, uint8, []byte) (int, error) {
	return 0, errDarwinUSBUnsupported
}

// BulkRead reads from one IN endpoint.
func (d *Device) BulkRead(context.Context, uint8, []byte) (int, error) {
	return 0, errDarwinUSBUnsupported
}
