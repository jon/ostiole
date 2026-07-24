package usb

import (
	"context"
	"errors"
	"time"
	"unsafe"
)

const usbfsControl = 0xc0185500

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
func (d *Device) ControlTransfer(
	ctx context.Context,
	requestType, request uint8,
	value, index uint16,
	data []byte,
) (int, error) {
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
	if len(data) > 0 {
		control.Data = uintptr(unsafe.Pointer(&data[0]))
	}
	count, err := d.runIOCTL(usbfsControl, &control)
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
	milliseconds := (timeout + time.Millisecond - 1) / time.Millisecond
	return uint32(milliseconds), nil
}
