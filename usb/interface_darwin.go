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
	maxPacketSize               uint16
}

type darwinInterfaceHandle interface {
	openSeize() error
	setAlternate(uint8) error
	pipes() ([]darwinPipe, error)
	close() error
}

type darwinInterfaceProvider interface {
	interfaceHandle(uint8) (darwinInterfaceHandle, error)
}

// ClaimInterface claims one interface and returns the claim. If cleanup after
// an error remains pending, Device.Close retries it.
func (d *Device) ClaimInterface(iface uint8) (*ClaimedInterface, error) {
	if d.handle == nil {
		return nil, errDarwinDeviceClosed
	}
	if d.claim != nil {
		return nil, fmt.Errorf("usb: interface %d is already claimed", d.claim.number)
	}
	provider, ok := d.handle.(darwinInterfaceProvider)
	if !ok {
		return nil, errors.New("usb: device cannot provide USB interfaces")
	}
	handle, err := provider.interfaceHandle(iface)
	if err != nil {
		return nil, fmt.Errorf("usb: find interface %d: %w", iface, err)
	}
	claim := &ClaimedInterface{device: d, number: iface}
	d.iface, d.claim = handle, claim
	if err := handle.openSeize(); err != nil {
		claimErr := fmt.Errorf("usb: claim interface %d: %w", iface, err)
		return nil, errors.Join(claimErr, claim.Close())
	}
	pipes, err := handle.pipes()
	if err != nil {
		pipesErr := fmt.Errorf("usb: enumerate interface %d pipes: %w", iface, err)
		return nil, errors.Join(pipesErr, claim.Close())
	}
	d.replaceRoutes(pipes)
	return claim, nil
}

func (d *Device) setAltSetting(claim *ClaimedInterface, alternate uint8) error {
	if d.claim != claim {
		return errors.New("usb: claimed interface is not owned by this device")
	}
	d.routes = nil
	if err := d.iface.setAlternate(alternate); err != nil {
		return fmt.Errorf("usb: select interface %d alternate %d: %w", claim.number, alternate, err)
	}
	pipes, err := d.iface.pipes()
	if err != nil {
		return fmt.Errorf("usb: enumerate interface %d pipes: %w", claim.number, err)
	}
	d.replaceRoutes(pipes)
	return nil
}

func (d *Device) replaceRoutes(pipes []darwinPipe) {
	d.routes = make(map[uint8]darwinPipe, len(pipes))
	for _, pipe := range pipes {
		d.routes[pipe.endpoint] = pipe
	}
}

func (d *Device) releaseInterface(claim *ClaimedInterface) error {
	if d.claim != claim {
		return errors.New("usb: claimed interface is not owned by this device")
	}
	if err := d.iface.close(); err != nil {
		return fmt.Errorf("usb: release interface %d: %w", claim.number, err)
	}
	d.iface, d.routes, d.claim = nil, nil, nil
	claim.device = nil
	return nil
}
