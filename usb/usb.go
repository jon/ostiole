// Package usb provides pure-Go USB access for Ostiole.
package usb

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
