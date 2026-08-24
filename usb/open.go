//go:build linux

package usb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Device is one open Linux usbfs attachment.
type Device struct {
	file          *os.File
	identity      DeviceInfo
	ioctl         ioctlFunc
	newBulkPoller func(uintptr) (linuxBulkPoller, error)
	claim         *ClaimedInterface
	bulk          sync.Mutex
	closeOnce     sync.Once
	closeErr      error
}

// Open opens the exact attachment selected during enumeration.
func (e *Enumerator) Open(ctx context.Context, info DeviceInfo) (*Device, error) {
	if ctx == nil {
		return nil, errors.New("usb: nil open context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil {
		return nil, errors.New("usb: nil enumerator")
	}
	if err := e.revalidate(ctx, info); err != nil {
		return nil, err
	}
	path := filepath.Join(e.devRoot, fmt.Sprintf("%03d", info.Bus), fmt.Sprintf("%03d", info.Address))
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("usb: open USB attachment: %w", err)
	}
	if err := e.revalidate(ctx, info); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &Device{file: file, identity: info}, nil
}

// Identity returns the attachment identity established by Open.
func (d *Device) Identity() DeviceInfo {
	if d == nil {
		return DeviceInfo{}
	}
	return d.identity
}

func (e *Enumerator) revalidate(ctx context.Context, expected DeviceInfo) error {
	entries, err := os.ReadDir(e.sysfsRoot)
	if err != nil {
		return fmt.Errorf("usb: read USB inventory: %w", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, ok, err := e.readDevice(entry)
		if err != nil {
			return err
		}
		if !ok || info.Bus != expected.Bus || info.Address != expected.Address {
			continue
		}
		if info.VID != expected.VID || info.PID != expected.PID {
			return fmt.Errorf("%w: bus %d address %d changed identity", ErrStaleCandidate, expected.Bus, expected.Address)
		}
		return nil
	}
	return fmt.Errorf("%w: bus %d address %d disappeared", ErrStaleCandidate, expected.Bus, expected.Address)
}

// Close releases the open usbfs attachment. If interface release fails, the
// device remains open and Close can be retried.
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
		if d.file != nil {
			d.closeErr = d.file.Close()
		}
	})
	return d.closeErr
}
