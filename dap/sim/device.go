package sim

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jon/ostiole/dap"
)

// ErrBusFault reports a target-bus access which completed with an error.
// The accepted AP transfer sets STICKYERR; subsequent SWD requests report FAULT.
// It does not represent a request-phase WAIT or a host transport failure.
var ErrBusFault = errors.New("dap/sim: target bus fault")

// MemoryDevice receives one complete target access at an absolute byte address.
// Data is in increasing address order, independent of MEM-AP byte order, and
// has length 1, 2, 4 or 8. Read fills data; Write consumes it without retaining
// the slice. Calls are serialized with their Target and all other shared views.
// Return ErrBusFault for a modeled bus error, including a rejected width.
// Other errors become ErrDeviceFailure. No access is replayed.
type MemoryDevice interface {
	Read(ctx context.Context, addr uint64, data []byte) error
	Write(ctx context.Context, addr uint64, data []byte) error
}

type deviceMapping struct {
	start, end uint64
	device     MemoryDevice
}

// MapMEMAPDevice maps size bytes of one MEM-AP to device before any target
// traffic. Mappings cannot overlap or change after traffic starts. Unmapped
// addresses retain the ordinary byte-memory fixture behavior. The mapping owns
// no cleanup; the caller owns device and serializes all uses of shared devices.
func (t *Target) MapMEMAPDevice(sel dap.APSel, addr, size uint64, device MemoryDevice) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	if t.started {
		return errors.New("dap/sim: device mapping after target traffic")
	}
	if _, err := t.selectorValue(sel); err != nil {
		return err
	}
	ap := t.aps[sel]
	if ap == nil || !ap.memAP {
		return errors.New("dap/sim: device mapping requires a MEM-AP")
	}
	if size == 0 || addr+size-1 < addr {
		return errors.New("dap/sim: empty or overflowing device mapping")
	}
	if nilDevice(device) {
		return errors.New("dap/sim: nil memory device")
	}
	end := addr + size - 1
	if ap.overlapsDevice(addr, end) {
		return errors.New("dap/sim: overlapping device mapping")
	}
	ap.devices = append(ap.devices, deviceMapping{addr, end, device})
	return nil
}

func nilDevice(device MemoryDevice) bool {
	if device == nil {
		return true
	}
	v := reflect.ValueOf(device)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (ap *accessPort) overlapsDevice(start, end uint64) bool {
	for _, mapping := range ap.devices {
		if start <= mapping.end && end >= mapping.start {
			return true
		}
	}
	return false
}

func (ap *accessPort) deviceForAccess(addr uint64, width int) (MemoryDevice, error) {
	end, err := memoryRangeEnd(addr, width)
	if err != nil || addr%uint64(width) != 0 {
		return nil, ErrBusFault
	}
	for _, mapping := range ap.devices {
		if addr > mapping.end || end < mapping.start {
			continue
		}
		if addr < mapping.start || end > mapping.end {
			return nil, fmt.Errorf("%w: access crosses device mapping", ErrBusFault)
		}
		return mapping.device, nil
	}
	return nil, nil
}

func (ap *accessPort) readMemory(ctx context.Context, addr uint64, width int) ([]byte, error) {
	device, err := ap.deviceForAccess(addr, width)
	if err != nil {
		return nil, err
	}
	data := make([]byte, width)
	if device != nil {
		return data, deviceResult(device.Read(ctx, addr, data))
	}
	for i := range data {
		data[i] = ap.memory[addr+uint64(i)]
	}
	return data, nil
}

func (ap *accessPort) writeMemory(ctx context.Context, addr uint64, data []byte) error {
	device, err := ap.deviceForAccess(addr, len(data))
	if err != nil {
		return err
	}
	if device != nil {
		return deviceResult(device.Write(ctx, addr, data))
	}
	for i, b := range data {
		ap.memory[addr+uint64(i)] = b
	}
	return nil
}
