package ftdi

import "github.com/jon/ostiole/usb"

// Candidate pairs a detached attachment with one supported MPSSE port.
type Candidate struct {
	Device usb.DeviceInfo
	Port   Port
}

// Candidates preserves every supported A/B binding without opening hardware.
// A matching USB identity does not prove that a board wires the port for SWD.
func Candidates(devices []usb.DeviceInfo) []Candidate {
	var candidates []Candidate
	for _, device := range devices {
		if !supportedDevice(device) {
			continue
		}
		candidates = append(candidates, Candidate{device, PortA})
		if device.PID != PIDFT232H {
			candidates = append(candidates, Candidate{device, PortB})
		}
	}
	return candidates
}
