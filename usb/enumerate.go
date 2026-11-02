//go:build linux

package usb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Enumerator reads one explicit snapshot of the Linux USB inventory.
type Enumerator struct {
	sysfsRoot string
	devRoot   string
	readFile  func(string) ([]byte, error)
	openFile  func(string, int, os.FileMode) (*os.File, error)
}

// New constructs an enumerator for the host USB inventory.
func New() *Enumerator {
	return newEnumerator("/sys/bus/usb/devices", "/dev/bus/usb")
}

func newEnumerator(sysfsRoot, devRoot string) *Enumerator {
	return &Enumerator{sysfsRoot: sysfsRoot, devRoot: devRoot, readFile: os.ReadFile, openFile: os.OpenFile}
}

// List returns devices matching one of the supplied filters. Disappearing or
// incomplete sysfs entries are skipped; other I/O failures remain errors.
func (e *Enumerator) List(ctx context.Context, filters []DeviceFilter) ([]DeviceInfo, error) {
	if ctx == nil {
		return nil, errors.New("usb: nil enumeration context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil {
		return nil, errors.New("usb: nil enumerator")
	}
	if err := validateFilters(filters); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(e.sysfsRoot)
	if err != nil {
		return nil, fmt.Errorf("usb: read USB inventory: %w", err)
	}
	devices := make([]DeviceInfo, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, ok, err := e.readMatchingDevice(entry, filters)
		if err != nil {
			return nil, err
		}
		if ok {
			devices = append(devices, info)
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Bus != devices[j].Bus {
			return devices[i].Bus < devices[j].Bus
		}
		return devices[i].Address < devices[j].Address
	})
	return devices, nil
}

func (e *Enumerator) readMatchingDevice(entry os.DirEntry, filters []DeviceFilter) (DeviceInfo, bool, error) {
	info, ok, err := e.readDevice(entry)
	if err != nil || !ok || !matchesAny(info, filters) {
		return DeviceInfo{}, false, err
	}
	info.Product, info.Serial, err = e.readStrings(entry)
	if attachmentDisappeared(err) {
		return DeviceInfo{}, false, nil
	}
	return info, err == nil, err
}

func (e *Enumerator) readDevice(entry os.DirEntry) (DeviceInfo, bool, error) {
	root := filepath.Join(e.sysfsRoot, entry.Name())
	info, err := os.Stat(root)
	if attachmentDisappeared(err) {
		return DeviceInfo{}, false, nil
	}
	if err != nil {
		return DeviceInfo{}, false, fmt.Errorf("usb: inspect USB entry: %w", err)
	}
	if !info.IsDir() {
		return DeviceInfo{}, false, nil
	}
	vendor, present, err := e.readNumber(root, "idVendor", 16, 16)
	if err != nil || !present {
		return DeviceInfo{}, false, err
	}
	product, present, err := e.readNumber(root, "idProduct", 16, 16)
	if err != nil || !present {
		return DeviceInfo{}, false, err
	}
	bus, present, err := e.readNumber(root, "busnum", 10, 8)
	if err != nil || !present {
		return DeviceInfo{}, false, err
	}
	address, present, err := e.readNumber(root, "devnum", 10, 8)
	if err != nil || !present {
		return DeviceInfo{}, false, err
	}
	return DeviceInfo{
		VID:     uint16(vendor),
		PID:     uint16(product),
		Bus:     uint8(bus),
		Address: uint8(address),
	}, true, nil
}

func (e *Enumerator) readStrings(entry os.DirEntry) (string, string, error) {
	root := filepath.Join(e.sysfsRoot, entry.Name())
	product, err := e.readText(root, "product")
	if err != nil {
		return "", "", err
	}
	serial, err := e.readText(root, "serial")
	return product, serial, err
}

func (e *Enumerator) readText(root, name string) (string, error) {
	value, err := e.readFile(filepath.Join(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("usb: read %s: %w", name, err)
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r"), nil
}

func (e *Enumerator) readNumber(root, name string, base, bits int) (uint64, bool, error) {
	value, err := e.readFile(filepath.Join(root, name))
	if attachmentDisappeared(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("usb: read %s: %w", name, err)
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(string(value)), base, bits)
	if err != nil {
		return 0, true, fmt.Errorf("usb: parse %s: %w", name, err)
	}
	return parsed, true, nil
}

func matchesAny(info DeviceInfo, filters []DeviceFilter) bool {
	for _, filter := range filters {
		if filter.matches(info) {
			return true
		}
	}
	return false
}

func attachmentDisappeared(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENODEV)
}
