//go:build darwin && cgo

package usb

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include "iokit_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
)

type iokitInventory struct{}

type iokitDevice struct {
	native *C.ostiole_usb_device
}

func (iokitInventory) snapshot() ([]darwinAttachment, error) {
	var iterator C.io_iterator_t
	result := C.ostiole_usb_iterator(&iterator)
	if result != C.kIOReturnSuccess {
		return nil, fmt.Errorf("IOKit result %#x", uint32(result))
	}
	defer C.IOObjectRelease(C.io_object_t(iterator))

	attachments := make([]darwinAttachment, 0)
	for {
		service := C.IOIteratorNext(iterator)
		if service == 0 {
			return attachments, nil
		}
		var native C.ostiole_usb_attachment
		ok := C.ostiole_usb_attachment_read(service, &native)
		C.IOObjectRelease(C.io_object_t(service))
		if ok == 0 {
			continue
		}
		attachments = append(attachments, darwinAttachment{
			vid:      uint16(native.vid),
			pid:      uint16(native.pid),
			location: uint32(native.location),
			address:  uint8(native.address),
		})
	}
}

func (iokitInventory) open(location uint32) (darwinDeviceHandle, error) {
	var iterator C.io_iterator_t
	result := C.ostiole_usb_iterator(&iterator)
	if result != C.kIOReturnSuccess {
		return nil, iokitError(result)
	}
	defer C.IOObjectRelease(C.io_object_t(iterator))
	for {
		service := C.IOIteratorNext(iterator)
		if service == 0 {
			return nil, ErrStaleCandidate
		}
		var attachment C.ostiole_usb_attachment
		if C.ostiole_usb_attachment_read(service, &attachment) == 0 ||
			uint32(attachment.location) != location {
			C.IOObjectRelease(C.io_object_t(service))
			continue
		}
		var openResult C.kern_return_t
		native := C.ostiole_usb_device_open(service, &openResult)
		if native == nil {
			C.IOObjectRelease(C.io_object_t(service))
			return nil, iokitError(openResult)
		}
		return &iokitDevice{native: native}, nil
	}
}

func (d *iokitDevice) identity() (darwinAttachment, error) {
	var native C.ostiole_usb_attachment
	if C.ostiole_usb_attachment_read(d.native.service, &native) == 0 {
		return darwinAttachment{}, errors.New("missing USB registry identity")
	}
	return darwinAttachment{
		vid:      uint16(native.vid),
		pid:      uint16(native.pid),
		location: uint32(native.location),
		address:  uint8(native.address),
	}, nil
}

func (d *iokitDevice) close() error {
	results := C.ostiole_usb_device_close(d.native)
	return joinIOKitCleanupCodes(
		uint32(results.device_close),
		uint32(results.service_release),
	)
}

func iokitError(result C.kern_return_t) error {
	return iokitErrorCode(uint32(result))
}

func joinIOKitCleanupCodes(codes ...uint32) error {
	var result error
	for _, code := range codes {
		if code != uint32(C.kIOReturnSuccess) {
			result = errors.Join(result, iokitErrorCode(code))
		}
	}
	return result
}

func iokitErrorCode(code uint32) error {
	return fmt.Errorf("IOKit result %#x", code)
}
