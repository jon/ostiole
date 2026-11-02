//go:build linux

package usb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestIncompleteSysfsAttachmentsAreSkipped(t *testing.T) {
	for _, attribute := range []string{"idVendor", "idProduct", "busnum", "devnum"} {
		t.Run(attribute, func(t *testing.T) {
			root := t.TempDir()
			writeSysfsDevice(t, root, "1-1", "0403", "6014", "1", "7")
			writeSysfsDevice(t, root, "2-1", "0d28", "0204", "2", "3")
			if err := os.Remove(filepath.Join(root, "1-1", attribute)); err != nil {
				t.Fatal(err)
			}
			got, err := newEnumerator(root, t.TempDir()).List(t.Context(), []DeviceFilter{AllDevices()})
			if err != nil || len(got) != 1 || got[0].VID != 0x0d28 || got[0].Bus != 2 || got[0].Address != 3 {
				t.Fatalf("incomplete attachment escaped: %+v, %v", got, err)
			}
		})
	}
}

func TestMissingSelectedUSBNodeIsStaleWithoutRevalidation(t *testing.T) {
	root := t.TempDir()
	writeSysfsDevice(t, root, "1-1", "0403", "6014", "1", "7")
	e := newEnumerator(root, t.TempDir())
	d, err := e.Open(t.Context(), DeviceInfo{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7})
	if d != nil || !errors.Is(err, ErrStaleCandidate) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing selected node: %v, %v", d, err)
	}
}

func TestDisappearingSysfsAttributesDoNotPoisonInventory(t *testing.T) {
	for _, attribute := range []string{"idVendor", "idProduct", "busnum", "devnum", "product", "serial"} {
		t.Run(attribute, func(t *testing.T) {
			root := t.TempDir()
			writeSysfsDevice(t, root, "1-1", "0403", "6014", "1", "7")
			writeSysfsDevice(t, root, "2-1", "0d28", "0204", "2", "3")
			e := newEnumerator(root, t.TempDir())
			e.readFile = func(path string) ([]byte, error) {
				if path == filepath.Join(root, "1-1", attribute) {
					return nil, &os.PathError{Op: "read", Path: path, Err: syscall.ENODEV}
				}
				return os.ReadFile(path)
			}
			got, err := e.List(t.Context(), []DeviceFilter{AllDevices()})
			if err != nil || len(got) != 1 || got[0].VID != 0x0d28 {
				t.Fatalf("disappearing entry: %+v, %v", got, err)
			}
			expected := DeviceInfo{VID: 0x0d28, PID: 0x0204, Bus: 2, Address: 3}
			if err := e.revalidate(t.Context(), expected); err != nil {
				t.Fatalf("unrelated removal blocked selected attachment: %v", err)
			}
			expected = DeviceInfo{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7}
			if err := e.revalidate(t.Context(), expected); !errors.Is(err, ErrStaleCandidate) {
				t.Fatalf("selected disappearance: %v", err)
			}
		})
	}
}

func TestSysfsIOFailuresRemainAttributable(t *testing.T) {
	for _, cause := range []error{syscall.EACCES, syscall.EIO} {
		for _, attribute := range []string{"idVendor", "idProduct", "busnum", "devnum", "product", "serial"} {
			t.Run(fmt.Sprintf("%v/%s", cause, attribute), func(t *testing.T) {
				root := t.TempDir()
				writeSysfsDevice(t, root, "1-1", "0403", "6014", "1", "7")
				e := newEnumerator(root, t.TempDir())
				e.readFile = func(path string) ([]byte, error) {
					if filepath.Base(path) == attribute {
						return nil, &os.PathError{Op: "read", Path: path, Err: cause}
					}
					return os.ReadFile(path)
				}
				if _, err := e.List(t.Context(), []DeviceFilter{AllDevices()}); !errors.Is(err, cause) || !strings.Contains(err.Error(), attribute) {
					t.Fatalf("attribute cause lost: %v", err)
				}
			})
		}
	}
}

func TestNodeOpenLossPreservesCauseAndDoesNotReplay(t *testing.T) {
	for _, cause := range []error{syscall.ENOENT, syscall.ENODEV, syscall.EACCES, syscall.EIO} {
		t.Run(fmt.Sprint(cause), func(t *testing.T) {
			root := t.TempDir()
			writeSysfsDevice(t, root, "1-1", "0403", "6014", "1", "7")
			e := newEnumerator(root, t.TempDir())
			reads, atOpen, opens := 0, 0, 0
			e.readFile = func(path string) ([]byte, error) { reads++; return os.ReadFile(path) }
			e.openFile = func(path string, _ int, _ os.FileMode) (*os.File, error) {
				opens++
				atOpen = reads
				return nil, &os.PathError{Op: "open", Path: path, Err: cause}
			}
			d, err := e.Open(t.Context(), DeviceInfo{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 7})
			stale := cause == syscall.ENOENT || cause == syscall.ENODEV
			if d != nil || !errors.Is(err, cause) || errors.Is(err, ErrStaleCandidate) != stale || opens != 1 || reads != atOpen {
				t.Fatalf("node open result: %v %v, reads%d atopen%d opens%d", d, err, reads, atOpen, opens)
			}
		})
	}
}
