package jlink

import "github.com/jon/ostiole/usb"

// Candidates copies attachments accepted by the reviewed USB product catalog.
// A candidate still requires interface validation when opened.
func Candidates(devices []usb.DeviceInfo) []usb.DeviceInfo {
	var candidates []usb.DeviceInfo
	for _, device := range devices {
		if supportedDevice(device) {
			candidates = append(candidates, device)
		}
	}
	return candidates
}
