//go:build linux

package usb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEnumeratorListsMatchingUSBDevicesWithOptionalStrings(t *testing.T) {
	root := t.TempDir()
	writeSysfsDevice(t, root, "1-2", "0403", "6014", "1", "7")
	writeSysfsDevice(t, root, "2-1", "0403", "6011", "2", "3")
	writeSysfsDevice(t, root, "3-4", "1234", "5678", "3", "9")
	if err := os.WriteFile(filepath.Join(root, "1-2", "product"), []byte("FT232H\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "1-2", "serial"), []byte("FT1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "1-2:1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	enumerator := newEnumerator(root, "/dev/bus/usb")

	got, err := enumerator.List(context.Background(), []DeviceFilter{
		ExactDevice(0x0403, 0x6014),
		ExactDevice(0x0403, 0x6011),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []DeviceInfo{
		{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7, Product: "FT232H", Serial: "FT1234"},
		{VID: 0x0403, PID: 0x6011, Bus: 2, Address: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func TestEnumeratorListsAllUSBDevices(t *testing.T) {
	root := t.TempDir()
	writeSysfsDevice(t, root, "2-1", "0d28", "0204", "2", "3")
	writeSysfsDevice(t, root, "1-2", "0403", "6014", "1", "7")
	if err := os.WriteFile(filepath.Join(root, "2-1", "product"), []byte("DAPLink CMSIS-DAP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	enumerator := newEnumerator(root, "/dev/bus/usb")

	got, err := enumerator.List(context.Background(), []DeviceFilter{AllDevices()})
	if err != nil {
		t.Fatal(err)
	}
	want := []DeviceInfo{
		{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7},
		{VID: 0x0d28, PID: 0x0204, Bus: 2, Address: 3, Product: "DAPLink CMSIS-DAP"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func TestEnumeratorIgnoresUnreadableSerialOnUnrelatedDevice(t *testing.T) {
	root := t.TempDir()
	writeSysfsDevice(t, root, "1-2", "0403", "6014", "1", "7")
	writeSysfsDevice(t, root, "2-1", "1234", "5678", "2", "3")
	if err := os.Mkdir(filepath.Join(root, "2-1", "serial"), 0o755); err != nil {
		t.Fatal(err)
	}
	enumerator := newEnumerator(root, "/dev/bus/usb")

	got, err := enumerator.List(context.Background(), []DeviceFilter{ExactDevice(0x0403, 0x6014)})
	if err != nil {
		t.Fatal(err)
	}
	want := []DeviceInfo{{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func TestEnumeratorIgnoresUnreadableProductOnUnrelatedDevice(t *testing.T) {
	root := t.TempDir()
	writeSysfsDevice(t, root, "1-2", "0403", "6014", "1", "7")
	writeSysfsDevice(t, root, "2-1", "1234", "5678", "2", "3")
	if err := os.Mkdir(filepath.Join(root, "2-1", "product"), 0o755); err != nil {
		t.Fatal(err)
	}
	enumerator := newEnumerator(root, "/dev/bus/usb")

	got, err := enumerator.List(context.Background(), []DeviceFilter{ExactDevice(0x0403, 0x6014)})
	if err != nil {
		t.Fatal(err)
	}
	want := []DeviceInfo{{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func TestEnumeratorRejectsInvalidFilterBeforeReadingSysfs(t *testing.T) {
	_, err := newEnumerator(filepath.Join(t.TempDir(), "missing"), "/dev/bus/usb").List(t.Context(), []DeviceFilter{{}})
	if err == nil || !strings.Contains(err.Error(), "invalid device filter") {
		t.Fatalf("List() error = %v, want invalid device filter", err)
	}
}

func TestEnumeratorHonorsCancellationBeforeReadingSysfs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newEnumerator(t.TempDir(), "/dev/bus/usb").List(ctx, []DeviceFilter{VendorDevices(0x0403)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("List() error = %v, want context.Canceled", err)
	}
}

func TestEnumeratorFollowsSysfsDeviceSymlinks(t *testing.T) {
	inventory := t.TempDir()
	backing := t.TempDir()
	writeSysfsDevice(t, backing, "device", "0403", "6014", "1", "3")
	if err := os.Symlink(filepath.Join(backing, "device"), filepath.Join(inventory, "1-1.2")); err != nil {
		t.Fatal(err)
	}

	got, err := newEnumerator(inventory, "/dev/bus/usb").List(context.Background(), []DeviceFilter{ExactDevice(0x0403, 0x6014)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Bus != 1 || got[0].Address != 3 {
		t.Fatalf("List() = %#v, want the symlinked USB device", got)
	}
}

func writeSysfsDevice(t *testing.T, root, name, vendor, product, bus, address string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for filename, value := range map[string]string{
		"idVendor":  vendor,
		"idProduct": product,
		"busnum":    bus,
		"devnum":    address,
	} {
		if err := os.WriteFile(filepath.Join(directory, filename), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
