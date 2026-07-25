package usb

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	usbfsClaimInterface   = 0x8004550f
	usbfsReleaseInterface = 0x80045510
	usbfsSetInterface     = 0x80085504
)

type ioctlFunc func(
	fd, request uintptr,
	argument any,
) (uintptr, error)

// ClaimInterface claims one interface for this device.
func (d *Device) ClaimInterface(iface uint8) error {
	if d.hasClaim {
		return fmt.Errorf(
			"usb: interface %d is already claimed",
			d.claimed,
		)
	}
	value := uint32(iface)
	if _, err := d.runIOCTL(usbfsClaimInterface, &value); err != nil {
		return fmt.Errorf("usb: claim interface %d: %w", iface, err)
	}
	d.claimed, d.hasClaim = iface, true
	return nil
}

// SetAltSetting selects an alternate setting on the claimed interface.
func (d *Device) SetAltSetting(iface, alternate uint8) error {
	if !d.hasClaim || d.claimed != iface {
		return fmt.Errorf("usb: interface %d is not claimed", iface)
	}
	value := [2]uint32{uint32(iface), uint32(alternate)}
	if _, err := d.runIOCTL(usbfsSetInterface, &value); err != nil {
		return fmt.Errorf(
			"usb: select interface %d alternate %d: %w",
			iface,
			alternate,
			err,
		)
	}
	return nil
}

// ReleaseInterface releases the claimed interface.
func (d *Device) ReleaseInterface(iface uint8) error {
	if !d.hasClaim || d.claimed != iface {
		return fmt.Errorf("usb: interface %d is not claimed", iface)
	}
	value := uint32(iface)
	if _, err := d.runIOCTL(usbfsReleaseInterface, &value); err != nil {
		return fmt.Errorf("usb: release interface %d: %w", iface, err)
	}
	d.hasClaim = false
	return nil
}

func (d *Device) runIOCTL(
	request uintptr,
	argument any,
) (uintptr, error) {
	ioctl := d.ioctl
	if ioctl == nil {
		ioctl = usbfsIOCTL
	}
	return ioctl(d.file.Fd(), request, argument)
}

func usbfsIOCTL(
	fd, request uintptr,
	argument any,
) (uintptr, error) {
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
		return 0, fmt.Errorf(
			"usb: unsupported ioctl argument %T",
			argument,
		)
	}
	result, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		request,
		uintptr(pointer),
	)
	if errno != 0 {
		return 0, errno
	}
	return result, nil
}
