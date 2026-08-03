//go:build darwin && cgo

package usb

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include "iokit_darwin.h"
*/
import "C"

import "fmt"

type iokitInventory struct{}

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
