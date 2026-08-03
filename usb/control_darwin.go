//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

const darwinDefaultTransferTimeout = 5 * time.Second

type darwinControlRequest struct {
	requestType, request uint8
	value, index         uint16
	timeout              uint32
}

type darwinControlDevice interface {
	control(darwinControlRequest, []byte) (uint16, error)
}

// ControlTransfer performs one deadline-bounded endpoint-zero transfer.
func (d *Device) ControlTransfer(ctx context.Context, requestType, request uint8, value, index uint16, data []byte) (int, error) {
	if d.handle == nil {
		return 0, errDarwinDeviceClosed
	}
	if len(data) > math.MaxUint16 {
		return 0, errors.New("usb: control buffer exceeds USB limit")
	}
	timeout, err := darwinTransferTimeout(ctx, time.Now())
	if err != nil {
		return 0, err
	}
	native, ok := d.handle.(darwinControlDevice)
	if !ok {
		return 0, errors.New("usb: device cannot send control transfers")
	}
	count, err := native.control(darwinControlRequest{
		requestType: requestType,
		request:     request,
		value:       value,
		index:       index,
		timeout:     timeout,
	}, data)
	if err != nil {
		return 0, fmt.Errorf("usb: control transfer: %w", err)
	}
	return int(count), nil
}

func darwinTransferTimeout(ctx context.Context, now time.Time) (uint32, error) {
	if ctx == nil {
		return 0, errors.New("usb: nil transfer context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	timeout := darwinDefaultTransferTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = deadline.Sub(now)
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
