package usb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRevalidatesAndOwnsExactUSBDevice(t *testing.T) {
	sysfs := t.TempDir()
	devices := t.TempDir()
	writeSysfsDevice(t, sysfs, "1-2", "0403", "6014", "1", "7")
	path := filepath.Join(devices, "001", "007")
	if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	enumerator := newEnumerator(sysfs, devices)
	info := DeviceInfo{
		VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7,
	}

	device, err := enumerator.Open(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if err := device.Close(); err != nil {
		t.Fatalf("second Close() failed: %v", err)
	}
}

func TestOpenRejectsChangedUSBIdentity(t *testing.T) {
	sysfs := t.TempDir()
	writeSysfsDevice(t, sysfs, "1-2", "1234", "5678", "1", "7")
	enumerator := newEnumerator(sysfs, t.TempDir())

	device, err := enumerator.Open(context.Background(), DeviceInfo{
		VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7,
	})
	if device != nil || !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("Open() = (%T, %v), want nil/ErrStaleCandidate", device, err)
	}
}
