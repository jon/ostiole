//go:build linux

package usb

import (
	"context"
	"errors"
	"math"
	"runtime"
	"time"
	"unsafe"
)

const usbfsControl = usbfsIOCRead | usbfsIOCWrite | uintptr(0x5500) | unsafe.Sizeof(usbControlTransfer{})<<16

const defaultTransferTimeout = 5 * time.Second

type usbControlTransfer struct {
	RequestType uint8
	Request     uint8
	Value       uint16
	Index       uint16
	Length      uint16
	Timeout     uint32
	Data        uintptr
}

// ControlTransfer performs one deadline-bounded endpoint-zero transfer.
// Buffers may contain at most 65,535 bytes. Without a context deadline, the
// transfer timeout is five seconds. Positive deadlines round up to milliseconds
// and clamp to the host's 32-bit millisecond limit.
func (d *Device) ControlTransfer(ctx context.Context, requestType, request uint8, value, index uint16, data []byte) (int, error) {
	if len(data) > math.MaxUint16 {
		return 0, errors.New("usb: control buffer exceeds USB limit")
	}
	timeout, err := transferTimeout(ctx)
	if err != nil {
		return 0, err
	}
	control := usbControlTransfer{
		RequestType: requestType,
		Request:     request,
		Value:       value,
		Index:       index,
		Length:      uint16(len(data)),
		Timeout:     timeout,
	}
	var pinned runtime.Pinner
	if len(data) > 0 {
		pinned.Pin(&data[0])
		defer pinned.Unpin()
		control.Data = uintptr(unsafe.Pointer(&data[0]))
	}
	count, err := d.runIOCTL(usbfsControl, &control)
	runtime.KeepAlive(data)
	return int(count), err
}

func transferTimeout(ctx context.Context) (uint32, error) {
	if ctx == nil {
		return 0, errors.New("usb: nil transfer context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	timeout := defaultTransferTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return 0, context.DeadlineExceeded
		}
	}
	milliseconds := timeout / time.Millisecond
	if timeout%time.Millisecond != 0 {
		milliseconds++
	}
	if milliseconds > math.MaxUint32 {
		milliseconds = math.MaxUint32
	}
	return uint32(milliseconds), nil
}
