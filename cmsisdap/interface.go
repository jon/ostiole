package cmsisdap

import (
	"errors"
	"fmt"

	"github.com/jon/ostiole/usb"
)

// ErrNoV2Interface reports that an active USB configuration has no CMSIS-DAP
// v2 command interface.
var ErrNoV2Interface = errors.New("cmsisdap: no CMSIS-DAP v2 command interface")

type commandInterface struct {
	number, alternate uint8
	bulkOut, bulkIn   usb.Endpoint
	swoIn             usb.Endpoint
}

func findCommandInterface(configuration usb.Configuration) (commandInterface, error) {
	var matches []commandInterface
	for _, iface := range configuration.Interfaces {
		for _, alternate := range iface.Alternates {
			if match, ok := commandInterfaceCandidate(iface.Number, alternate); ok {
				matches = append(matches, match)
			}
		}
	}
	if len(matches) == 0 {
		return commandInterface{}, ErrNoV2Interface
	}
	if len(matches) != 1 {
		return commandInterface{}, fmt.Errorf("cmsisdap: active USB configuration has %d CMSIS-DAP v2 command interfaces", len(matches))
	}
	return matches[0], nil
}

func commandInterfaceCandidate(number uint8, alternate usb.AlternateSetting) (commandInterface, bool) {
	if alternate.Class != 0xff || alternate.Subclass != 0 || alternate.Protocol != 0 {
		return commandInterface{}, false
	}
	if len(alternate.Endpoints) != 2 && len(alternate.Endpoints) != 3 {
		return commandInterface{}, false
	}
	if !usableBulkEndpoint(alternate.Endpoints[0], false) || !usableBulkEndpoint(alternate.Endpoints[1], true) {
		return commandInterface{}, false
	}
	result := commandInterface{
		number: number, alternate: alternate.Number,
		bulkOut: alternate.Endpoints[0], bulkIn: alternate.Endpoints[1],
	}
	if len(alternate.Endpoints) == 3 {
		if !usableBulkEndpoint(alternate.Endpoints[2], true) || alternate.Endpoints[2].Address == result.bulkIn.Address {
			return commandInterface{}, false
		}
		result.swoIn = alternate.Endpoints[2]
	}
	return result, true
}

func usableBulkEndpoint(endpoint usb.Endpoint, input bool) bool {
	return endpoint.Address&0x0f != 0 && endpoint.Address&0x80 != 0 == input &&
		endpoint.TransferType == usb.TransferBulk && endpoint.MaxPacketSize != 0
}
