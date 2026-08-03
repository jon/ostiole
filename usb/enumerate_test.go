package usb

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnumeratorListsMatchingUSBDevicesWithoutStrings(t *testing.T) {
	root := t.TempDir()
	writeSysfsDevice(t, root, "1-2", "0403", "6014", "1", "7")
	writeSysfsDevice(t, root, "2-1", "0403", "6011", "2", "3")
	writeSysfsDevice(t, root, "3-4", "1234", "5678", "3", "9")
	if err := os.Mkdir(filepath.Join(root, "1-2:1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	enumerator := newEnumerator(root, "/dev/bus/usb")

	got, err := enumerator.List(context.Background(), []DeviceFilter{
		{VID: 0x0403, PID: 0x6014},
		{VID: 0x0403, PID: 0x6011},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []DeviceInfo{
		{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7},
		{VID: 0x0403, PID: 0x6011, Bus: 2, Address: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func TestEnumeratorHonorsCancellationBeforeReadingSysfs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newEnumerator(t.TempDir(), "/dev/bus/usb").List(ctx, []DeviceFilter{{VID: 0x0403}})
	if err != context.Canceled {
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

	got, err := newEnumerator(inventory, "/dev/bus/usb").List(context.Background(), []DeviceFilter{{
		VID: 0x0403,
		PID: 0x6014,
	}})
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
