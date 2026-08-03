//go:build linux

package usb

import (
	"context"
	"os"
	"testing"
)

func TestDevicePerformsBoundedBulkTransfers(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "usb-device")
	if err != nil {
		t.Fatal(err)
	}
	device := &Device{file: file}
	var endpoints []uint32
	device.ioctl = func(_ uintptr, request uintptr, argument any) (uintptr, error) {
		if request != usbfsBulk {
			t.Fatalf("ioctl request = %#x, want %#x", request, usbfsBulk)
		}
		transfer := argument.(*usbBulkTransfer)
		endpoints = append(endpoints, transfer.Endpoint)
		if transfer.Length != 4 || transfer.Timeout == 0 || transfer.Data == 0 {
			t.Fatalf("bulk transfer = %#v", transfer)
		}
		return uintptr(transfer.Length), nil
	}

	if count, err := device.BulkWrite(context.Background(), 0x02, []byte{1, 2, 3, 4}); err != nil || count != 4 {
		t.Fatalf("BulkWrite() = %d, %v", count, err)
	}
	if count, err := device.BulkRead(context.Background(), 0x81, make([]byte, 4)); err != nil || count != 4 {
		t.Fatalf("BulkRead() = %d, %v", count, err)
	}
	if len(endpoints) != 2 || endpoints[0] != 0x02 || endpoints[1] != 0x81 {
		t.Fatalf("bulk endpoints = %#v", endpoints)
	}
}

func TestDeviceRejectsWrongBulkDirectionBeforeIO(t *testing.T) {
	device := &Device{}
	called := false
	device.ioctl = func(uintptr, uintptr, any) (uintptr, error) {
		called = true
		return 0, nil
	}

	if _, err := device.BulkWrite(context.Background(), 0x81, []byte{1}); err == nil {
		t.Fatal("BulkWrite accepted an IN endpoint")
	}
	if _, err := device.BulkRead(context.Background(), 0x02, make([]byte, 1)); err == nil {
		t.Fatal("BulkRead accepted an OUT endpoint")
	}
	if called {
		t.Fatal("wrong-direction bulk transfer issued an ioctl")
	}
}
