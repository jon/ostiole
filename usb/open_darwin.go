//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type darwinDeviceHandle interface {
	identity() (darwinAttachment, error)
	close() error
}

type darwinOpener interface {
	open(uint32) (darwinDeviceHandle, error)
}

// Device is one open macOS USB attachment.
type Device struct {
	handle    darwinDeviceHandle
	identity  DeviceInfo
	iface     darwinInterfaceHandle
	routes    map[uint8]darwinPipe
	claim     *ClaimedInterface
	closeOnce sync.Once
	closeErr  error
}

// Open opens the exact attachment selected during enumeration.
func (e *Enumerator) Open(ctx context.Context, expected DeviceInfo) (*Device, error) {
	if ctx == nil {
		return nil, errors.New("usb: nil open context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil {
		return nil, errors.New("usb: nil enumerator")
	}
	if e.inventory == nil {
		return nil, errors.New("usb: uninitialized enumerator")
	}
	attachment, err := e.find(ctx, expected)
	if err != nil {
		return nil, err
	}
	opener, ok := e.inventory.(darwinOpener)
	if !ok {
		return nil, errors.New("usb: inventory cannot open attachments")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, err := opener.open(attachment.location)
	if err != nil {
		return nil, fmt.Errorf("usb: open USB attachment: %w", err)
	}
	actual, identityErr := handle.identity()
	if identityErr != nil {
		revalidateErr := fmt.Errorf("usb: revalidate open attachment: %w", identityErr)
		return nil, errors.Join(revalidateErr, handle.close())
	}
	if err := validateDarwinIdentity(expected, actual.info()); err != nil {
		return nil, errors.Join(err, handle.close())
	}
	return &Device{handle: handle, identity: actual.info()}, nil
}

// Identity returns the attachment identity established by Open.
func (d *Device) Identity() DeviceInfo {
	if d == nil {
		return DeviceInfo{}
	}
	return d.identity
}

func (e *Enumerator) find(ctx context.Context, expected DeviceInfo) (darwinAttachment, error) {
	attachments, err := e.inventory.snapshot()
	if err != nil {
		return darwinAttachment{}, fmt.Errorf("usb: read USB inventory: %w", err)
	}
	for _, attachment := range attachments {
		if err := ctx.Err(); err != nil {
			return darwinAttachment{}, err
		}
		actual := attachment.info()
		if actual.Bus != expected.Bus || actual.Address != expected.Address {
			continue
		}
		if err := validateDarwinIdentity(expected, actual); err != nil {
			return darwinAttachment{}, err
		}
		return attachment, nil
	}
	return darwinAttachment{}, fmt.Errorf("%w: bus %d address %d disappeared",
		ErrStaleCandidate, expected.Bus, expected.Address)
}

func validateDarwinIdentity(expected, actual DeviceInfo) error {
	if actual.Bus != expected.Bus || actual.Address != expected.Address ||
		actual.VID != expected.VID || actual.PID != expected.PID {
		return fmt.Errorf("%w: bus %d address %d changed identity",
			ErrStaleCandidate, expected.Bus, expected.Address)
	}
	return nil
}

// Close releases the open macOS USB attachment. If interface release fails,
// the device remains open and Close can be retried.
func (d *Device) Close() error {
	if d == nil {
		return nil
	}
	if d.claim != nil {
		if err := d.claim.Close(); err != nil {
			return err
		}
	}
	d.closeOnce.Do(func() {
		handle := d.handle
		d.handle = nil
		if handle != nil {
			d.closeErr = handle.close()
		}
	})
	return d.closeErr
}
