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
)

// Enumerator reads one explicit snapshot of the Linux USB inventory.
type Enumerator struct {
	sysfsRoot string
	devRoot   string
}

// New constructs an enumerator for the host USB inventory.
func New() *Enumerator {
	return newEnumerator("/sys/bus/usb/devices", "/dev/bus/usb")
}

func newEnumerator(sysfsRoot, devRoot string) *Enumerator {
	return &Enumerator{sysfsRoot: sysfsRoot, devRoot: devRoot}
}

// List returns devices matching one of the supplied filters.
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
	info.Serial, err = e.readSerial(entry)
	return info, err == nil, err
}

func (e *Enumerator) readDevice(entry os.DirEntry) (DeviceInfo, bool, error) {
	root := filepath.Join(e.sysfsRoot, entry.Name())
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return DeviceInfo{}, false, nil
	}
	if err != nil {
		return DeviceInfo{}, false, fmt.Errorf("usb: inspect USB entry: %w", err)
	}
	if !info.IsDir() {
		return DeviceInfo{}, false, nil
	}
	vendor, present, err := readNumber(root, "idVendor", 16, 16)
	if err != nil || !present {
		return DeviceInfo{}, false, err
	}
	product, _, err := readNumber(root, "idProduct", 16, 16)
	if err != nil {
		return DeviceInfo{}, false, err
	}
	bus, _, err := readNumber(root, "busnum", 10, 8)
	if err != nil {
		return DeviceInfo{}, false, err
	}
	address, _, err := readNumber(root, "devnum", 10, 8)
	if err != nil {
		return DeviceInfo{}, false, err
	}
	return DeviceInfo{
		VID:     uint16(vendor),
		PID:     uint16(product),
		Bus:     uint8(bus),
		Address: uint8(address),
	}, true, nil
}

func (e *Enumerator) readSerial(entry os.DirEntry) (string, error) {
	return readText(filepath.Join(e.sysfsRoot, entry.Name()), "serial")
}

func readText(root, name string) (string, error) {
	value, err := os.ReadFile(filepath.Join(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("usb: read %s: %w", name, err)
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r"), nil
}

func readNumber(root, name string, base, bits int) (uint64, bool, error) {
	value, err := os.ReadFile(filepath.Join(root, name))
	if errors.Is(err, os.ErrNotExist) {
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
