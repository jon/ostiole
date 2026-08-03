//go:build linux

package usb

import (
	"os"
	"testing"
)

type ioctlRecord struct {
	request   uintptr
	first     uint32
	alternate uint32
}

func TestDeviceClaimsSelectsAndReleasesOneInterface(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "usb-device")
	if err != nil {
		t.Fatal(err)
	}
	device := &Device{file: file}
	var records []ioctlRecord
	device.ioctl = func(_ uintptr, request uintptr, argument any) (uintptr, error) {
		record := ioctlRecord{request: request}
		if request == usbfsSetInterface {
			values := argument.(*[2]uint32)
			record.first, record.alternate = values[0], values[1]
		} else {
			record.first = *argument.(*uint32)
		}
		records = append(records, record)
		return 0, nil
	}

	if err := device.ClaimInterface(1); err != nil {
		t.Fatal(err)
	}
	if err := device.SetAltSetting(1, 3); err != nil {
		t.Fatal(err)
	}
	if err := device.ReleaseInterface(1); err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 ||
		records[0] != (ioctlRecord{request: usbfsClaimInterface, first: 1}) ||
		records[1] != (ioctlRecord{
			request: usbfsSetInterface, first: 1, alternate: 3,
		}) ||
		records[2] != (ioctlRecord{request: usbfsReleaseInterface, first: 1}) {
		t.Fatalf("ioctls = %#v", records)
	}
}

func TestDeviceRejectsAnotherClaimBeforeIO(t *testing.T) {
	device := &Device{hasClaim: true, claimed: 0}
	called := false
	device.ioctl = func(uintptr, uintptr, any) (uintptr, error) {
		called = true
		return 0, nil
	}

	if err := device.ClaimInterface(1); err == nil {
		t.Fatal("second ClaimInterface succeeded")
	}
	if called {
		t.Fatal("second ClaimInterface issued an ioctl")
	}
}
