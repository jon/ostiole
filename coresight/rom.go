package coresight

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotROMTable means the component does not advertise a supported ROM architecture.
var ErrNotROMTable = errors.New("coresight: not a ROM table")

// ROMTable describes a ROM table's entry layout, derived from a component identity.
// It owns no reader or resources. Its zero value is invalid.
type ROMTable struct {
	base   uint64
	count  int
	stride uint64
	class  uint8
}

// ROMTable interprets a valid identification snapshot without memory traffic.
// It recognizes class 1 and Arm class 9 ROM architecture 0x0af7, revision 0.
// Unknown class 9 entry formats or revisions return errors, not ErrNotROMTable.
func (c Component) ROMTable() (ROMTable, error) {
	if c.Base&0xfff != 0 || c.CIDR&0xffff0fff != 0xb105000d {
		return ROMTable{}, errors.New("coresight: invalid component snapshot")
	}
	table := ROMTable{base: c.Base, count: 960, stride: 4, class: c.Class()}
	if c.Class() == 1 {
		return table, nil
	}
	arch, present := c.Architecture()
	if !present || arch.Architect != 0x23b || arch.ID != 0x0af7 {
		return ROMTable{}, ErrNotROMTable
	}
	if arch.Revision != 0 || c.DEVID&0xf > 1 {
		return ROMTable{}, fmt.Errorf("coresight: unsupported ROM revision %d or format %#x", arch.Revision, c.DEVID&0xf)
	}
	table.count = 512
	if c.DEVID&0xf == 1 {
		table.count = 256
		table.stride = 8
	}
	return table, nil
}

// EntryCount returns the architectural entry capacity, not a discovered length.
// A zero return means the table is invalid.
func (t ROMTable) EntryCount() int { return t.count }

// ROMEntry is a detached entry snapshot. Base and power metadata are meaningful
// only when Present is true. End marks a terminator, not an absent interior entry.
// PowerID is scoped to the containing table and is valid only with PowerIDValid.
// A power ID does not establish that the child is powered or safe to access.
type ROMEntry struct {
	Raw          uint64
	Base         uint64
	Present      bool
	End          bool
	PowerID      uint8
	PowerIDValid bool
}

// ReadEntry reads one architectural entry using numeric 32-bit scalars. It
// validates the index and arguments before traffic, reads both words of a
// 64-bit entry before decoding, and returns a zero entry on failure. Reserved
// encodings, zero offsets in present entries, and address underflow or overflow fail.
// It does not read the child, request power, or acquire cleanup obligations.
func (t ROMTable) ReadEntry(ctx context.Context, reader ScalarReader, index int) (ROMEntry, error) {
	if ctx == nil || reader == nil {
		return ROMEntry{}, errors.New("coresight: nil context or scalar reader")
	}
	if index < 0 || index >= t.count {
		return ROMEntry{}, fmt.Errorf("coresight: invalid ROM entry index %d", index)
	}
	address := t.base + uint64(index)*t.stride
	low, err := readWord(ctx, reader, address)
	if err != nil {
		return ROMEntry{}, err
	}
	raw := uint64(low)
	if t.stride == 8 {
		high, err := readWord(ctx, reader, address+4)
		if err != nil {
			return ROMEntry{}, err
		}
		raw |= uint64(high) << 32
	}
	entry, err := t.decode(raw)
	if err != nil {
		return ROMEntry{}, fmt.Errorf("coresight: ROM entry at %#x: %w", address, err)
	}
	return entry, nil
}

func (t ROMTable) decode(raw uint64) (ROMEntry, error) {
	e := ROMEntry{Raw: raw, End: raw == 0}
	if e.End {
		return e, nil
	}
	if t.class == 9 && raw&3 == 2 {
		return e, nil
	}
	if raw&2 == 0 || t.class == 9 && raw&3 != 3 {
		return ROMEntry{}, errors.New("invalid entry format or presence")
	}
	if raw&0xe08 != 0 || raw&4 == 0 && raw&0x1f0 != 0 {
		return ROMEntry{}, errors.New("reserved entry bits are nonzero")
	}
	e.Present = raw&1 != 0
	if !e.Present {
		return e, nil
	}
	var err error
	e.Base, err = t.entryBase(raw)
	if err != nil {
		return ROMEntry{}, err
	}
	e.PowerID = uint8(raw >> 4 & 0x1f)
	e.PowerIDValid = raw&4 != 0
	return e, nil
}

func (t ROMTable) entryBase(raw uint64) (uint64, error) {
	offset := int64(raw &^ 0xfff)
	if t.stride == 4 {
		offset = int64(int32(raw &^ 0xfff))
	}
	base := t.base + uint64(offset)
	if offset == 0 || offset > 0 && base < t.base || offset < 0 && base > t.base {
		return 0, errors.New("zero offset or address outside uint64 range")
	}
	return base, nil
}
