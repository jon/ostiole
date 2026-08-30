package cmsisdap_test

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/jon/ostiole/cmsisdap"
	"github.com/jon/ostiole/usb"
)

var errCMSISDAPHILUnavailable = errors.New("CMSIS-DAP HIL unavailable")

func selectCMSISDAPHILCandidate(devices []usb.DeviceInfo, selection, serial string) (usb.DeviceInfo, error) {
	if selection != "" && serial != "" {
		return usb.DeviceInfo{}, errors.New("set only one CMSIS-DAP HIL device or serial selector")
	}
	if serial != "" {
		return selectCMSISDAPSerial(devices, serial)
	}
	if selection != "" {
		return selectCMSISDAPAddress(devices, selection)
	}
	candidates := cmsisdap.Candidates(devices)
	if len(candidates) != 1 {
		return usb.DeviceInfo{}, fmt.Errorf("%w: found %d product-string candidates, want one or an exact device or serial selector", errCMSISDAPHILUnavailable, len(candidates))
	}
	return candidates[0], nil
}

func selectCMSISDAPSerial(devices []usb.DeviceInfo, serial string) (usb.DeviceInfo, error) {
	var matches []usb.DeviceInfo
	for _, device := range devices {
		if device.Serial == serial {
			matches = append(matches, device)
		}
	}
	if len(matches) != 1 {
		return usb.DeviceInfo{}, fmt.Errorf("%w: found %d USB attachments with the requested serial, want one", errCMSISDAPHILUnavailable, len(matches))
	}
	return matches[0], nil
}

func selectCMSISDAPAddress(devices []usb.DeviceInfo, selection string) (usb.DeviceInfo, error) {
	parts := strings.Split(selection, ":")
	if len(parts) != 2 {
		return usb.DeviceInfo{}, fmt.Errorf("invalid CMSIS-DAP HIL device %q, want bus:address", selection)
	}
	bus, busErr := strconv.ParseUint(parts[0], 10, 8)
	address, addressErr := strconv.ParseUint(parts[1], 10, 8)
	if busErr != nil || addressErr != nil {
		return usb.DeviceInfo{}, fmt.Errorf("invalid CMSIS-DAP HIL device %q, want bus:address", selection)
	}
	for _, device := range devices {
		if device.Bus == uint8(bus) && device.Address == uint8(address) {
			return device, nil
		}
	}
	return usb.DeviceInfo{}, fmt.Errorf("%w: USB attachment %s is not present", errCMSISDAPHILUnavailable, selection)
}

func TestSelectCMSISDAPHILCandidate(t *testing.T) {
	devices := []usb.DeviceInfo{
		{Bus: 1, Address: 2, Product: "CMSIS-DAP v2", Serial: "first"},
		{Bus: 1, Address: 3, Product: "composite probe", Serial: "second"},
		{Bus: 1, Address: 4, Product: "another CMSIS-DAP", Serial: "third"},
	}
	if got, err := selectCMSISDAPHILCandidate(devices[:1], "", ""); err != nil || got != devices[0] {
		t.Fatalf("implicit selection = (%#v, %v)", got, err)
	}
	if got, err := selectCMSISDAPHILCandidate(devices, "", "second"); err != nil || got != devices[1] {
		t.Fatalf("serial selection = (%#v, %v)", got, err)
	}
	if got, err := selectCMSISDAPHILCandidate(devices, "1:3", ""); err != nil || got != devices[1] {
		t.Fatalf("address selection = (%#v, %v)", got, err)
	}
	if _, err := selectCMSISDAPHILCandidate(devices, "1:2", "first"); err == nil || errors.Is(err, errCMSISDAPHILUnavailable) {
		t.Fatalf("conflicting selectors error = %v", err)
	}
	if _, err := selectCMSISDAPHILCandidate(devices, "", ""); !errors.Is(err, errCMSISDAPHILUnavailable) {
		t.Fatalf("ambiguous implicit selection error = %v", err)
	}
	if _, err := selectCMSISDAPHILCandidate(devices, "bad", ""); err == nil || errors.Is(err, errCMSISDAPHILUnavailable) {
		t.Fatalf("invalid address error = %v", err)
	}
	if _, err := selectCMSISDAPHILCandidate(devices, "1:9", ""); !errors.Is(err, errCMSISDAPHILUnavailable) {
		t.Fatalf("missing address error = %v", err)
	}
	if _, err := selectCMSISDAPHILCandidate(devices, "", "missing"); !errors.Is(err, errCMSISDAPHILUnavailable) {
		t.Fatalf("missing serial error = %v", err)
	}
}
