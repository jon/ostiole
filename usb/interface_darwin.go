//go:build darwin && cgo

package usb

import (
	"errors"
	"fmt"
)

const darwinBulkPipe = 2

var errDarwinDeviceClosed = errors.New("usb: device is closed")

type darwinPipe struct {
	endpoint, ref, transferType uint8
}

type darwinInterfaceHandle interface {
	openSeize() error
	pipes() ([]darwinPipe, error)
	close() error
}

type darwinInterfaceProvider interface {
	interfaceHandle(uint8) (darwinInterfaceHandle, error)
}

// ClaimInterface claims one interface for this device.
func (d *Device) ClaimInterface(iface uint8) error {
	if d.handle == nil {
		return errDarwinDeviceClosed
	}
	if d.hasClaim {
		return fmt.Errorf("usb: interface %d is already claimed", d.claimed)
	}
	provider, ok := d.handle.(darwinInterfaceProvider)
	if !ok {
		return errors.New("usb: device cannot provide USB interfaces")
	}
	handle, err := provider.interfaceHandle(iface)
	if err != nil {
		return fmt.Errorf("usb: find interface %d: %w", iface, err)
	}
	if err := handle.openSeize(); err != nil {
		claimErr := fmt.Errorf("usb: claim interface %d: %w", iface, err)
		return errors.Join(claimErr, handle.close())
	}
	pipes, err := handle.pipes()
	if err != nil {
		pipesErr := fmt.Errorf("usb: enumerate interface %d pipes: %w", iface, err)
		return errors.Join(pipesErr, handle.close())
	}
	d.iface, d.claimed, d.hasClaim = handle, iface, true
	d.routes = make(map[uint8]darwinPipe, len(pipes))
	for _, pipe := range pipes {
		d.routes[pipe.endpoint] = pipe
	}
	return nil
}

// ReleaseInterface releases the claimed interface.
func (d *Device) ReleaseInterface(iface uint8) error {
	if !d.hasClaim || d.claimed != iface {
		return fmt.Errorf("usb: interface %d is not claimed", iface)
	}
	handle := d.iface
	d.iface, d.routes, d.hasClaim = nil, nil, false
	if err := handle.close(); err != nil {
		return fmt.Errorf("usb: release interface %d: %w", iface, err)
	}
	return nil
}
