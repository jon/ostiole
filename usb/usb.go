// Package usb provides native host USB access for Ostiole.
//
// Linux uses sysfs and usbfs directly. macOS uses the system IOKit and
// CoreFoundation frameworks through cgo and requires the macOS SDK at build
// time.
package usb

import (
	"errors"
	"fmt"
)

// ErrStaleCandidate reports that an attachment changed after enumeration.
var ErrStaleCandidate = errors.New("usb: stale candidate")

// ErrNotConfigured reports that a USB device has no active configuration.
var ErrNotConfigured = errors.New("usb: device is not configured")

// DeviceFilter selects either one exact USB identity or every product from one
// vendor. Its zero value is invalid.
type DeviceFilter struct {
	kind filterKind
	vid  uint16
	pid  uint16
}

type filterKind uint8

const (
	filterExact filterKind = iota + 1
	filterVendor
)

// ExactDevice selects one exact vendor and product ID. Product ID zero is an
// exact product value, not a wildcard.
func ExactDevice(vid, pid uint16) DeviceFilter {
	return DeviceFilter{kind: filterExact, vid: vid, pid: pid}
}

// VendorDevices selects every product from one vendor.
func VendorDevices(vid uint16) DeviceFilter {
	return DeviceFilter{kind: filterVendor, vid: vid}
}

func (f DeviceFilter) valid() bool {
	return f.kind == filterExact || f.kind == filterVendor
}

func (f DeviceFilter) matches(info DeviceInfo) bool {
	if info.VID != f.vid {
		return false
	}
	return f.kind == filterVendor || f.kind == filterExact && info.PID == f.pid
}

func validateFilters(filters []DeviceFilter) error {
	for i, filter := range filters {
		if !filter.valid() {
			return fmt.Errorf("usb: invalid device filter at index %d", i)
		}
	}
	return nil
}

// DeviceInfo identifies one physical USB attachment. Serial is the host-visible
// USB serial number, or empty when the device or host does not provide one.
type DeviceInfo struct {
	VID, PID     uint16
	Bus, Address uint8
	Serial       string
}
