package usb

import (
	"context"
	"os"
	"testing"
)

func TestDevicePerformsBoundedControlTransfer(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "usb-device")
	if err != nil {
		t.Fatal(err)
	}
	device := &Device{file: file}
	var got *usbControlTransfer
	device.ioctl = func(_ uintptr, request uintptr, argument any) (uintptr, error) {
		if request != usbfsControl {
			t.Fatalf("ioctl request = %#x, want %#x", request, usbfsControl)
		}
		value := *argument.(*usbControlTransfer)
		got = &value
		return 0, nil
	}

	count, err := device.ControlTransfer(
		context.Background(),
		0x40,
		0x0b,
		0x0200,
		1,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || got == nil ||
		got.RequestType != 0x40 ||
		got.Request != 0x0b ||
		got.Value != 0x0200 ||
		got.Index != 1 ||
		got.Length != 0 ||
		got.Timeout == 0 {
		t.Fatalf("ControlTransfer() = %d, request %#v", count, got)
	}
}

func TestControlTransferHonorsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	device := &Device{}
	called := false
	device.ioctl = func(uintptr, uintptr, any) (uintptr, error) {
		called = true
		return 0, nil
	}

	if _, err := device.ControlTransfer(
		ctx, 0x40, 0x0b, 0x0200, 1, nil,
	); err != context.Canceled {
		t.Fatalf("ControlTransfer() error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("canceled ControlTransfer issued an ioctl")
	}
}
