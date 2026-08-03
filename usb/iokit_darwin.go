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
	"runtime"
	"unsafe"
)

type iokitInventory struct{}

type iokitDevice struct {
	native *C.ostiole_usb_device
}

type iokitInterface struct {
	native *C.ostiole_usb_interface
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
	return joinIOKitCleanupCodes(uint32(results.device_close), uint32(results.service_release))
}

func (d *iokitDevice) control(request darwinControlRequest, data []byte) (uint16, error) {
	var pointer unsafe.Pointer
	if len(data) > 0 {
		pointer = unsafe.Pointer(&data[0])
	}
	var done C.uint16_t
	result := C.ostiole_usb_device_control(d.native,
		C.uint8_t(request.requestType), C.uint8_t(request.request),
		C.uint16_t(request.value), C.uint16_t(request.index), pointer,
		C.uint16_t(len(data)), C.uint32_t(request.timeout), &done)
	runtime.KeepAlive(data)
	if result != C.kIOReturnSuccess {
		return 0, iokitError(result)
	}
	return uint16(done), nil
}

func (d *iokitDevice) interfaceHandle(iface uint8) (darwinInterfaceHandle, error) {
	var result C.kern_return_t
	native := C.ostiole_usb_find_interface(d.native, C.uint8_t(iface), &result)
	if native == nil {
		return nil, iokitError(result)
	}
	return &iokitInterface{native: native}, nil
}

func (i *iokitInterface) openSeize() error {
	result := C.ostiole_usb_interface_open_seize(i.native)
	if result != C.kIOReturnSuccess {
		return iokitError(result)
	}
	return nil
}

func (i *iokitInterface) setAlternate(alternate uint8) error {
	result := C.ostiole_usb_interface_set_alternate(i.native, C.uint8_t(alternate))
	if result != C.kIOReturnSuccess {
		return iokitError(result)
	}
	return nil
}

func (i *iokitInterface) pipes() ([]darwinPipe, error) {
	var count C.uint8_t
	result := C.ostiole_usb_interface_pipe_count(i.native, &count)
	if result != C.kIOReturnSuccess {
		return nil, iokitError(result)
	}
	pipes := make([]darwinPipe, 0, int(count))
	for ref := uint8(1); ref <= uint8(count); ref++ {
		var native C.ostiole_usb_pipe
		result = C.ostiole_usb_interface_pipe(i.native, C.uint8_t(ref), &native)
		if result != C.kIOReturnSuccess {
			return nil, iokitError(result)
		}
		pipes = append(pipes, darwinPipe{
			endpoint:     uint8(native.endpoint),
			ref:          uint8(native.ref),
			transferType: uint8(native.transfer_type),
		})
	}
	return pipes, nil
}

func (i *iokitInterface) close() error {
	result := C.ostiole_usb_interface_close(i.native)
	if result != C.kIOReturnSuccess {
		return iokitError(result)
	}
	return nil
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
