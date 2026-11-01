//go:build darwin && cgo

package usb

/*
#include "iokit_darwin.h"
*/
import "C"

import "runtime"

type darwinBulkEvent struct {
	available bool
	count     uint32
	err       error
}

type darwinBulkRequest interface {
	take([]byte) darwinBulkEvent
	free()
}

type darwinBulkNative interface {
	submit(uint8, bool, []byte) (darwinBulkRequest, error)
	poll()
	abort(uint8) error
	close()
}

type nativeIOKitBulk struct{ native *C.ostiole_usb_bulk_engine }
type nativeIOKitRequest struct{ native *C.ostiole_usb_bulk_transfer }

func (i *iokitInterface) openBulkNative() (darwinBulkNative, error) {
	var result C.kern_return_t
	native := C.ostiole_usb_bulk_engine_open(i.native, &result)
	if native == nil {
		return nil, iokitError(result)
	}
	return &nativeIOKitBulk{native: native}, nil
}

func (n *nativeIOKitBulk) submit(pipe uint8, input bool, buffer []byte) (darwinBulkRequest, error) {
	var result C.kern_return_t
	direction := C.uint8_t(0)
	if input {
		direction = 1
	}
	native := C.ostiole_usb_bulk_transfer_submit(n.native, C.uint8_t(pipe), direction, bytePointer(buffer), C.uint32_t(len(buffer)), &result)
	runtime.KeepAlive(buffer)
	if native == nil {
		return nil, iokitError(result)
	}
	return &nativeIOKitRequest{native: native}, nil
}

func (n *nativeIOKitBulk) poll() { C.ostiole_usb_bulk_engine_poll(n.native, 1) }
func (n *nativeIOKitBulk) abort(pipe uint8) error {
	return iokitError(C.ostiole_usb_bulk_engine_abort(n.native, C.uint8_t(pipe)))
}
func (n *nativeIOKitBulk) close() { C.ostiole_usb_bulk_engine_close(n.native) }

func (r *nativeIOKitRequest) take(buffer []byte) darwinBulkEvent {
	event := C.ostiole_usb_bulk_transfer_take(r.native, bytePointer(buffer), C.uint32_t(len(buffer)))
	runtime.KeepAlive(buffer)
	return darwinBulkEvent{
		available: event.available != 0,
		count:     uint32(event.transfer.done),
		err:       iokitTransferError(uint32(event.transfer.result), uint32(event.transfer.cleanup)),
	}
}
func (r *nativeIOKitRequest) free() { C.ostiole_usb_bulk_transfer_free(r.native) }
