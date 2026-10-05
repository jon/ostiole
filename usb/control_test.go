//go:build linux

package usb

import (
	"context"
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestDevicePerformsBoundedControlTransfer(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "usb-device")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
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

	count, err := device.ControlTransfer(context.Background(), 0x40, 0x0b, 0x0200, 1, nil)
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

func TestControlTransferNativeABI(t *testing.T) {
	wantSize, wantOffset, wantRequest := uintptr(24), uintptr(16), uintptr(0xc0185500)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		wantSize, wantOffset, wantRequest = 16, 12, 0xc0105500
	}
	if unsafe.Sizeof(usbControlTransfer{}) != wantSize || unsafe.Offsetof(usbControlTransfer{}.Data) != wantOffset {
		t.Fatal("control structure does not match the Linux USB ABI")
	}
	if uintptr(usbfsControl) != wantRequest {
		t.Fatalf("control request = %#x, want %#x", uintptr(usbfsControl), wantRequest)
	}
}

func TestControlTransferBufferBounds(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "usb-device")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	device := &Device{file: file}
	data := make([]byte, 65535)
	called := 0
	device.ioctl = func(_ uintptr, _ uintptr, argument any) (uintptr, error) {
		called++
		control := argument.(*usbControlTransfer)
		runtime.GC()
		if control.Length != 65535 || control.Data != uintptr(unsafe.Pointer(&data[0])) {
			t.Fatal("control transfer did not retain the complete buffer")
		}
		data[len(data)-1] = 0xa5
		return uintptr(len(data)), nil
	}
	count, err := device.ControlTransfer(context.Background(), 0x80, 1, 0, 0, data)
	if err != nil || count != len(data) || data[len(data)-1] != 0xa5 {
		t.Fatalf("ControlTransfer() = %d, %v", count, err)
	}
	if _, err := device.ControlTransfer(context.Background(), 0x80, 1, 0, 0, make([]byte, 65536)); err == nil {
		t.Fatal("oversized control buffer was accepted")
	}
	if called != 1 {
		t.Fatalf("ioctl calls = %d, want 1", called)
	}
}

func TestControlTransferDistantDeadline(t *testing.T) {
	for _, duration := range []time.Duration{50 * 24 * time.Hour, time.Duration(1<<63 - 1)} {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(duration))
		got, err := transferTimeout(ctx)
		cancel()
		if err != nil || got != 0xffffffff {
			t.Fatalf("timeout for %v = %d, %v; want 4294967295", duration, got, err)
		}
	}
}

func TestControlTransferPinsStackBuffer(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "control-input")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.WriteAt([]byte{0xa5}, 0); err != nil {
		t.Fatal(err)
	}
	device := &Device{file: file}
	device.ioctl = func(fd, _ uintptr, argument any) (uintptr, error) {
		control := argument.(*usbControlTransfer)
		return controlReadAfterStackGrowth(64, fd, control.Data)
	}
	done := make(chan error, 1)
	go func() { done <- controlTransferStackBuffer(device) }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func controlTransferStackBuffer(device *Device) error {
	var data [16]byte
	count, err := device.ControlTransfer(context.Background(), 0x80, 1, 0, 0, data[:])
	if err != nil {
		return err
	}
	if count != 1 || data[0] != 0xa5 {
		return errors.New("control input did not reach the caller's buffer after stack growth")
	}
	return nil
}

func controlReadAfterStackGrowth(depth int, fd, address uintptr) (uintptr, error) {
	var padding [4096]byte
	padding[0] = byte(depth)
	var count uintptr
	var err error
	if depth > 0 {
		count, err = controlReadAfterStackGrowth(depth-1, fd, address)
	} else {
		result, _, errno := syscall.Syscall(syscall.SYS_READ, fd, address, 1)
		count = result
		if errno != 0 {
			err = errno
		}
	}
	runtime.KeepAlive(padding)
	return count, err
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

	if _, err := device.ControlTransfer(ctx, 0x40, 0x0b, 0x0200, 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ControlTransfer() error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("canceled ControlTransfer issued an ioctl")
	}
}
