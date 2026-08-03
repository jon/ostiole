//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
)

var errDarwinUSBUnsupported = errors.New("usb: macOS host access is not implemented")

// BulkWrite writes to one OUT endpoint.
func (d *Device) BulkWrite(context.Context, uint8, []byte) (int, error) {
	return 0, errDarwinUSBUnsupported
}

// BulkRead reads from one IN endpoint.
func (d *Device) BulkRead(context.Context, uint8, []byte) (int, error) {
	return 0, errDarwinUSBUnsupported
}
