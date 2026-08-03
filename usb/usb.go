// Package usb provides native host USB access for Ostiole.
//
// Linux uses sysfs and usbfs directly. macOS uses the system IOKit and
// CoreFoundation frameworks through cgo and requires the macOS SDK at build
// time.
package usb

import (
	"errors"
)

// ErrStaleCandidate reports that an attachment changed after enumeration.
var ErrStaleCandidate = errors.New("usb: stale candidate")

// DeviceFilter selects devices by vendor and product ID.
// A zero PID matches any product from the selected vendor.
type DeviceFilter struct {
	VID, PID uint16
}

// DeviceInfo identifies one physical USB attachment.
type DeviceInfo struct {
	VID, PID     uint16
	Bus, Address uint8
}
