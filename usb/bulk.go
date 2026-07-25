package usb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"unsafe"
)

const usbfsBulk = 0xc0185502

type usbBulkTransfer struct {
	Endpoint uint32
	Length   uint32
	Timeout  uint32
	Data     uintptr
}

// BulkWrite writes to one OUT endpoint.
func (d *Device) BulkWrite(
	ctx context.Context,
	endpoint uint8,
	data []byte,
) (int, error) {
	if endpoint&0x80 != 0 {
		return 0, fmt.Errorf(
			"usb: bulk-write endpoint %#02x is an IN endpoint",
			endpoint,
		)
	}
	return d.bulkTransfer(ctx, endpoint, data)
}

// BulkRead reads from one IN endpoint.
func (d *Device) BulkRead(
	ctx context.Context,
	endpoint uint8,
	data []byte,
) (int, error) {
	if endpoint&0x80 == 0 {
		return 0, fmt.Errorf(
			"usb: bulk-read endpoint %#02x is an OUT endpoint",
			endpoint,
		)
	}
	return d.bulkTransfer(ctx, endpoint, data)
}

func (d *Device) bulkTransfer(
	ctx context.Context,
	endpoint uint8,
	data []byte,
) (int, error) {
	if uint64(len(data)) > math.MaxUint32 {
		return 0, errors.New("usb: bulk buffer exceeds usbfs limit")
	}
	timeout, err := transferTimeout(ctx)
	if err != nil {
		return 0, err
	}
	transfer := usbBulkTransfer{
		Endpoint: uint32(endpoint),
		Length:   uint32(len(data)),
		Timeout:  timeout,
	}
	if len(data) > 0 {
		transfer.Data = uintptr(unsafe.Pointer(&data[0]))
	}
	count, err := d.runIOCTL(usbfsBulk, &transfer)
	return int(count), err
}
