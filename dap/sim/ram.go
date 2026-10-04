package sim

import (
	"context"
	"errors"
)

// RAM is sparse byte-addressed memory which can be mapped through multiple
// MEM-APs. Its zero value is usable and contains no written bytes. Serialize all
// device accesses,
// fixture writes and snapshots, including accesses through other APs or targets.
// Mapping bounds belong to the Target; Bytes is a detached snapshot, not bus
// traffic. Unwritten bytes read as zero.
type RAM struct {
	memory map[uint64]byte
}

// SetBytes copies fixture bytes into RAM without a simulated bus access.
func (r *RAM) SetBytes(addr uint64, data []byte) error {
	if r == nil {
		return errors.New("dap/sim: nil RAM")
	}
	if _, err := memoryRangeEnd(addr, len(data)); err != nil {
		return err
	}
	if r.memory == nil {
		r.memory = make(map[uint64]byte)
	}
	for i, b := range data {
		r.memory[addr+uint64(i)] = b
	}
	return nil
}

// Bytes returns a detached copy of size bytes without a simulated bus access.
func (r *RAM) Bytes(addr uint64, size int) ([]byte, error) {
	if r == nil {
		return nil, errors.New("dap/sim: nil RAM")
	}
	if _, err := memoryRangeEnd(addr, size); err != nil {
		return nil, err
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = r.memory[addr+uint64(i)]
	}
	return data, nil
}

// Read fills data from one aligned 1, 2, 4 or 8 byte bus access.
func (r *RAM) Read(ctx context.Context, addr uint64, data []byte) error {
	if err := checkRAMAccess(ctx, addr, len(data)); err != nil {
		return err
	}
	value, err := r.Bytes(addr, len(data))
	if err != nil {
		return err
	}
	copy(data, value)
	return nil
}

// Write copies data from one aligned 1, 2, 4 or 8 byte bus access.
func (r *RAM) Write(ctx context.Context, addr uint64, data []byte) error {
	if err := checkRAMAccess(ctx, addr, len(data)); err != nil {
		return err
	}
	return r.SetBytes(addr, data)
}

func checkRAMAccess(ctx context.Context, addr uint64, width int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if width != 1 && width != 2 && width != 4 && width != 8 {
		return ErrBusFault
	}
	if _, err := memoryRangeEnd(addr, width); err != nil || addr%uint64(width) != 0 {
		return ErrBusFault
	}
	return nil
}
