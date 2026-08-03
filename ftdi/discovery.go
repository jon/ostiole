package ftdi

import "github.com/jon/ostiole/usb"

// SupportedDevices returns the USB identities accepted by Open.
func SupportedDevices() []usb.DeviceFilter {
	return []usb.DeviceFilter{
		{VID: VID, PID: PIDFT2232H},
		{VID: VID, PID: PIDFT4232H},
		{VID: VID, PID: PIDFT232H},
	}
}
