//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

type darwinBulkInterface interface {
	readPipe(uint8, []byte, uint32) (uint32, error)
	writePipe(uint8, []byte, uint32) error
}

// BulkWrite writes to one OUT endpoint.
func (d *Device) BulkWrite(ctx context.Context, endpoint uint8, data []byte) (int, error) {
	if endpoint&0x80 != 0 {
		return 0, fmt.Errorf("usb: bulk-write endpoint %#02x is an IN endpoint", endpoint)
	}
	pipe, native, timeout, err := d.prepareBulk(ctx, endpoint, len(data))
	if err != nil {
		return 0, err
	}
	if err := native.writePipe(pipe.ref, data, timeout); err != nil {
		return 0, fmt.Errorf("usb: bulk write: %w", err)
	}
	return len(data), nil
}

// BulkRead reads from one IN endpoint.
func (d *Device) BulkRead(ctx context.Context, endpoint uint8, data []byte) (int, error) {
	if endpoint&0x80 == 0 {
		return 0, fmt.Errorf("usb: bulk-read endpoint %#02x is an OUT endpoint", endpoint)
	}
	pipe, native, timeout, err := d.prepareBulk(ctx, endpoint, len(data))
	if err != nil {
		return 0, err
	}
	count, err := native.readPipe(pipe.ref, data, timeout)
	if err != nil {
		return 0, fmt.Errorf("usb: bulk read: %w", err)
	}
	return int(count), nil
}

func (d *Device) prepareBulk(ctx context.Context, endpoint uint8, length int) (darwinPipe, darwinBulkInterface, uint32, error) {
	if err := validateDarwinBulkLength(uint64(length)); err != nil {
		return darwinPipe{}, nil, 0, err
	}
	pipe, ok := d.routes[endpoint]
	if !ok {
		return darwinPipe{}, nil, 0, fmt.Errorf("usb: endpoint %#02x is not active", endpoint)
	}
	if pipe.transferType != darwinBulkPipe {
		return darwinPipe{}, nil, 0, fmt.Errorf("usb: endpoint %#02x is not bulk", endpoint)
	}
	native, ok := d.iface.(darwinBulkInterface)
	if !ok {
		return darwinPipe{}, nil, 0, errors.New("usb: interface cannot transfer bulk data")
	}
	timeout, err := darwinTransferTimeout(ctx, time.Now())
	return pipe, native, timeout, err
}

func validateDarwinBulkLength(length uint64) error {
	if length > math.MaxUint32 {
		return errors.New("usb: bulk buffer exceeds IOKit limit")
	}
	return nil
}
