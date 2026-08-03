//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

type darwinAttachment struct {
	vid, pid uint16
	location uint32
	address  uint8
}

func (a darwinAttachment) info() DeviceInfo {
	return DeviceInfo{
		VID:     a.vid,
		PID:     a.pid,
		Bus:     uint8(a.location >> 24),
		Address: a.address,
	}
}

type darwinInventory interface {
	snapshot() ([]darwinAttachment, error)
}

// Enumerator reads one explicit snapshot of the macOS USB inventory.
type Enumerator struct {
	inventory darwinInventory
}

// New constructs an enumerator for the host USB inventory.
func New() *Enumerator {
	return newDarwinEnumerator(iokitInventory{})
}

func newDarwinEnumerator(inventory darwinInventory) *Enumerator {
	return &Enumerator{inventory: inventory}
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
	if e.inventory == nil {
		return nil, errors.New("usb: uninitialized enumerator")
	}
	attachments, err := e.inventory.snapshot()
	if err != nil {
		return nil, fmt.Errorf("usb: read USB inventory: %w", err)
	}
	devices := make([]DeviceInfo, 0)
	for _, attachment := range attachments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info := attachment.info()
		if matchesAnyDarwin(info, filters) {
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

func matchesAnyDarwin(info DeviceInfo, filters []DeviceFilter) bool {
	for _, filter := range filters {
		if info.VID == filter.VID &&
			(filter.PID == 0 || info.PID == filter.PID) {
			return true
		}
	}
	return false
}
