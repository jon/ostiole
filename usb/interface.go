//go:build linux

package usb

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

const (
	usbfsClaimInterface   = 0x8004550f
	usbfsReleaseInterface = 0x80045510
	usbfsSetInterface     = 0x80085504
)

type ioctlFunc func(fd, request uintptr, argument any) (uintptr, error)

// ClaimInterface claims one interface and returns the claim. If cleanup after
// an error remains pending, Device.Close retries it.
func (d *Device) ClaimInterface(iface uint8) (*ClaimedInterface, error) {
	if d.claim != nil {
		return nil, fmt.Errorf("usb: interface %d is already claimed", d.claim.number)
	}
	value := uint32(iface)
	if _, err := d.runIOCTL(usbfsClaimInterface, &value); err != nil {
		return nil, fmt.Errorf("usb: claim interface %d: %w", iface, err)
	}
	claim := &ClaimedInterface{device: d, number: iface}
	d.claim = claim
	return claim, nil
}

func (d *Device) setAltSetting(claim *ClaimedInterface, alternate uint8) error {
	if d.claim != claim {
		return errors.New("usb: claimed interface is not owned by this device")
	}
	value := [2]uint32{uint32(claim.number), uint32(alternate)}
	if _, err := d.runIOCTL(usbfsSetInterface, &value); err != nil {
		return fmt.Errorf("usb: select interface %d alternate %d: %w", claim.number, alternate, err)
	}
	return nil
}

func (d *Device) releaseInterface(claim *ClaimedInterface) error {
	if d.claim != claim {
		return errors.New("usb: claimed interface is not owned by this device")
	}
	value := uint32(claim.number)
	if _, err := d.runIOCTL(usbfsReleaseInterface, &value); err != nil {
		return fmt.Errorf("usb: release interface %d: %w", claim.number, err)
	}
	d.claim = nil
	claim.device = nil
	return nil
}

func (d *Device) runIOCTL(request uintptr, argument any) (uintptr, error) {
	ioctl := d.ioctl
	if ioctl == nil {
		ioctl = usbfsIOCTL
	}
	return ioctl(d.file.Fd(), request, argument)
}

func usbfsIOCTL(fd, request uintptr, argument any) (uintptr, error) {
	var pointer unsafe.Pointer
	switch value := argument.(type) {
	case *uint32:
		pointer = unsafe.Pointer(value)
	case *[2]uint32:
		pointer = unsafe.Pointer(value)
	case *usbControlTransfer:
		pointer = unsafe.Pointer(value)
	case *usbBulkTransfer:
		pointer = unsafe.Pointer(value)
	default:
		return 0, fmt.Errorf("usb: unsupported ioctl argument %T", argument)
	}
	result, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(pointer))
	if errno != 0 {
		return 0, errno
	}
	return result, nil
}
