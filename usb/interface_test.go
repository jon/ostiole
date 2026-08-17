//go:build linux

package usb

import (
	"errors"
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

	claim, err := device.ClaimInterface(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.SetAltSetting(3); err != nil {
		t.Fatal(err)
	}
	if err := claim.Close(); err != nil {
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
	device := &Device{}
	device.claim = &ClaimedInterface{device: device, number: 0}
	called := false
	device.ioctl = func(uintptr, uintptr, any) (uintptr, error) {
		called = true
		return 0, nil
	}

	if _, err := device.ClaimInterface(1); err == nil {
		t.Fatal("second ClaimInterface succeeded")
	}
	if called {
		t.Fatal("second ClaimInterface issued an ioctl")
	}
}

func TestClaimedInterfaceRetainsOwnershipAfterFailedRelease(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "usb-device")
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("release failed")
	releases := 0
	device := &Device{file: file}
	device.ioctl = func(_ uintptr, request uintptr, _ any) (uintptr, error) {
		if request == usbfsReleaseInterface {
			releases++
			if releases == 1 {
				return 0, want
			}
		}
		return 0, nil
	}

	claim, err := device.ClaimInterface(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() error = %v, want %v", err, want)
	}
	if _, err := device.ClaimInterface(2); err == nil {
		t.Fatal("ClaimInterface() succeeded while release was pending")
	}
	if err := claim.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if _, err := device.ClaimInterface(2); err != nil {
		t.Fatalf("ClaimInterface() after release: %v", err)
	}
}
