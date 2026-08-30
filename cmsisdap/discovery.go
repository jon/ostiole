// Package cmsisdap recognizes and opens explicitly selected CMSIS-DAP v2
// probes over USB.
package cmsisdap

import (
	"strings"

	"github.com/jon/ostiole/usb"
)

const productMarker = "CMSIS-DAP"

// Candidates returns a detached list of attachments whose host-visible USB
// product string contains the case-sensitive CMSIS-DAP marker. A candidate
// still requires descriptor validation before use.
func Candidates(devices []usb.DeviceInfo) []usb.DeviceInfo {
	var candidates []usb.DeviceInfo
	for _, device := range devices {
		if strings.Contains(device.Product, productMarker) {
			candidates = append(candidates, device)
		}
	}
	return candidates
}
