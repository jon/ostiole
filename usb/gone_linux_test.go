//go:build linux

package usb

import (
	"errors"
	"syscall"
	"testing"
)

func TestLinuxDeviceGoneClassificationKeepsNativeCause(t *testing.T) {
	for _, cause := range []error{syscall.ENODEV, syscall.EIO, syscall.ETIMEDOUT} {
		d := &Device{ioctl: func(uintptr, uintptr, any) (uintptr, error) { return 0, cause }}
		_, err := d.runIOCTL(usbfsControl, &usbControlTransfer{})
		if !errors.Is(err, cause) || errors.Is(err, ErrDeviceGone) != errors.Is(cause, syscall.ENODEV) {
			t.Fatalf("ioctl cause=%v returned %v", cause, err)
		}
	}
	urb := &usbURB{Endpoint: 0x81, Status: -int32(syscall.ENODEV)}
	if err := usbURBStatusError(urb); !errors.Is(err, ErrDeviceGone) || !errors.Is(err, syscall.ENODEV) {
		t.Fatalf("gone completion=%v", err)
	}
}
