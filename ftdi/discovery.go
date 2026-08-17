package ftdi

import "github.com/jon/ostiole/usb"

// SupportedDevices returns the USB identities accepted by Open.
func SupportedDevices() []usb.DeviceFilter {
	return []usb.DeviceFilter{
		usb.ExactDevice(VID, PIDFT2232H),
		usb.ExactDevice(VID, PIDFT4232H),
		usb.ExactDevice(VID, PIDFT232H),
	}
}
