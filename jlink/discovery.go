// Package jlink drives explicitly selected SEGGER J-Link probes over USB.
package jlink

import (
	"errors"
	"fmt"

	"github.com/jon/ostiole/usb"
)

// VID is SEGGER's USB vendor identifier.
const VID = 0x1366

var supportedPIDs = []uint16{
	0x0101, 0x0102, 0x0103, 0x0104, 0x0105, 0x0107, 0x0108,
	0x1010, 0x1011, 0x1012, 0x1013, 0x1014, 0x1015, 0x1016, 0x1017, 0x1018,
	0x1020, 0x1024, 0x1025,
	0x1050, 0x1051, 0x1052, 0x1053, 0x1054, 0x1055, 0x1056, 0x1057, 0x1058, 0x1059,
	0x1060, 0x1061, 0x1062, 0x1063, 0x1064, 0x1065, 0x1066, 0x1067, 0x1068, 0x1069,
}

// SupportedDevices returns the exact USB identities accepted by Open.
func SupportedDevices() []usb.DeviceFilter {
	filters := make([]usb.DeviceFilter, len(supportedPIDs))
	for index, pid := range supportedPIDs {
		filters[index] = usb.ExactDevice(VID, pid)
	}
	return filters
}

func supportedDevice(info usb.DeviceInfo) bool {
	if info.VID != VID {
		return false
	}
	for _, pid := range supportedPIDs {
		if info.PID == pid {
			return true
		}
	}
	return false
}

type applicationInterface struct {
	number, alternate uint8
	bulkIn, bulkOut   usb.Endpoint
}

func findApplicationInterface(configuration usb.Configuration) (applicationInterface, error) {
	var matches []applicationInterface
	for _, iface := range configuration.Interfaces {
		for _, alternate := range iface.Alternates {
			if match, ok := applicationCandidate(iface.Number, alternate); ok {
				matches = append(matches, match)
			}
		}
	}
	if len(matches) == 0 {
		return applicationInterface{}, errors.New("jlink: active USB configuration has no J-Link application interface")
	}
	if len(matches) != 1 {
		return applicationInterface{}, fmt.Errorf("jlink: active USB configuration has %d J-Link application interfaces", len(matches))
	}
	return matches[0], nil
}

func applicationCandidate(number uint8, alternate usb.AlternateSetting) (applicationInterface, bool) {
	if alternate.Class != 0xff || alternate.Subclass != 0xff || alternate.Protocol != 0xff || len(alternate.Endpoints) != 2 {
		return applicationInterface{}, false
	}
	first, second := alternate.Endpoints[0], alternate.Endpoints[1]
	if !usableBulkEndpoint(first) || !usableBulkEndpoint(second) || first.Address&0x80 == second.Address&0x80 {
		return applicationInterface{}, false
	}
	result := applicationInterface{number: number, alternate: alternate.Number}
	if first.Address&0x80 != 0 {
		result.bulkIn, result.bulkOut = first, second
	} else {
		result.bulkIn, result.bulkOut = second, first
	}
	return result, result.bulkOut.Address != 0
}

func usableBulkEndpoint(endpoint usb.Endpoint) bool {
	return endpoint.Address&0x0f != 0 && endpoint.TransferType == usb.TransferBulk && endpoint.MaxPacketSize != 0
}
