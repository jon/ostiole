//go:build linux

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
	if err := os.WriteFile(filepath.Join(sysfs, "1-2", "product"), []byte("FT232H\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(devices, "001", "007")
	if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	enumerator := newEnumerator(sysfs, devices)
	info := DeviceInfo{
		VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7, Product: "FT232H",
	}

	device, err := enumerator.Open(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if got := device.Identity(); got != info {
		t.Fatalf("Identity() = %+v, want %+v", got, info)
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

func TestOpenRejectsChangedUSBSerial(t *testing.T) {
	sysfs := t.TempDir()
	writeSysfsDevice(t, sysfs, "1-2", "0403", "6014", "1", "7")
	if err := os.WriteFile(filepath.Join(sysfs, "1-2", "serial"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := newEnumerator(sysfs, t.TempDir()).revalidate(context.Background(), DeviceInfo{
		VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7, Serial: "old",
	})
	if !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("revalidate() error = %v, want ErrStaleCandidate", err)
	}
}

func TestOpenRejectsChangedUSBProduct(t *testing.T) {
	sysfs := t.TempDir()
	writeSysfsDevice(t, sysfs, "1-2", "0d28", "0204", "1", "7")
	if err := os.WriteFile(filepath.Join(sysfs, "1-2", "product"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := newEnumerator(sysfs, t.TempDir()).revalidate(context.Background(), DeviceInfo{
		VID: 0x0d28, PID: 0x0204, Bus: 1, Address: 7, Product: "old",
	})
	if !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("revalidate() error = %v, want ErrStaleCandidate", err)
	}
}

func TestOpenRevalidationIgnoresUnreadableSerialOnUnrelatedDevice(t *testing.T) {
	sysfs := t.TempDir()
	writeSysfsDevice(t, sysfs, "1-2", "1234", "5678", "1", "3")
	writeSysfsDevice(t, sysfs, "2-1", "0403", "6014", "2", "7")
	if err := os.Mkdir(filepath.Join(sysfs, "1-2", "serial"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := newEnumerator(sysfs, t.TempDir()).revalidate(context.Background(), DeviceInfo{
		VID: 0x0403, PID: 0x6014, Bus: 2, Address: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenRevalidationIgnoresUnreadableProductOnUnrelatedDevice(t *testing.T) {
	sysfs := t.TempDir()
	writeSysfsDevice(t, sysfs, "1-2", "1234", "5678", "1", "3")
	writeSysfsDevice(t, sysfs, "2-1", "0d28", "0204", "2", "7")
	if err := os.Mkdir(filepath.Join(sysfs, "1-2", "product"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := newEnumerator(sysfs, t.TempDir()).revalidate(context.Background(), DeviceInfo{
		VID: 0x0d28, PID: 0x0204, Bus: 2, Address: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
}
