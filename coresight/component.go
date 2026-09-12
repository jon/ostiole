// Package coresight identifies debug components through target memory.
// It borrows the reader and neither writes target memory nor owns cleanup.
package coresight

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/dap"
)

// ScalarReader reads aligned target scalars as numeric values, independently
// of target byte order. Identify uses only dap.Size32. A dap.MemAP implements
// this interface; its owner remains responsible for serialization and release.
type ScalarReader interface {
	ReadScalar(context.Context, uint64, dap.TransferSize) (uint64, error)
}

// Component is a detached identification snapshot. CIDR and PIDR pack the low
// byte of each numbered register at bit 8*n. DEVARCH, DEVID, and DEVTYPE are
// read only for class 9 and are zero for other classes. Reserved ID bits are
// ignored. Unknown classes and part numbers are retained without naming them.
type Component struct {
	Base    uint64 // Address of the 4 KiB identification page, not necessarily the component's first page.
	CIDR    uint32
	PIDR    uint64
	DEVARCH uint32
	DEVID   uint32
	DEVTYPE uint8
}

// Class returns the CIDR component class.
func (c Component) Class() uint8 { return uint8(c.CIDR >> 12 & 0xf) }

// Part returns the designer-assigned twelve-bit part number.
func (c Component) Part() uint16 { return uint16(c.PIDR & 0xfff) }

// Designer returns the packed designer code and whether PIDR2 identifies it
// as JEP106. Bits [10:7] hold the continuation count and [6:0] the identity.
// When jedec is false, callers must not interpret code as a JEP106 assignment.
func (c Component) Designer() (code uint16, jedec bool) {
	code = uint16(c.PIDR>>32&0xf)<<7 | uint16(c.PIDR>>12&0x7f)
	return code, c.PIDR&(1<<19) != 0
}

// Revision returns PIDR2.REVISION. PIDR3's REVAND and CMOD remain in PIDR.
func (c Component) Revision() uint8 { return uint8(c.PIDR >> 20 & 0xf) }

// Architecture identifies the architect, architecture ID, and revision
// advertised by a present class 9 DEVARCH register.
type Architecture struct {
	Architect uint16 // JEP106 continuation count in [10:7], identity in [6:0].
	ID        uint16
	Revision  uint8
}

// Architecture decodes DEVARCH only when the component is class 9 and its
// PRESENT bit is set. Other DEVARCH bits remain available in the raw snapshot.
func (c Component) Architecture() (Architecture, bool) {
	if c.Class() != 9 || c.DEVARCH&(1<<20) == 0 {
		return Architecture{}, false
	}
	return Architecture{
		Architect: uint16(c.DEVARCH >> 21),
		ID:        uint16(c.DEVARCH),
		Revision:  uint8(c.DEVARCH >> 16 & 0xf),
	}, true
}

// Identify reads one explicitly addressed component's identification registers.
// base must name a 4 KiB aligned identification page accessible to the reader.
// The caller must establish that this is a safe debug-component address; this
// function does not discover addresses or request component power or unlocks.
// It validates CIDR before reading PIDR, then reads DEVARCH, DEVID, and DEVTYPE
// for class 9. Any failure returns a zero Component and an error;
// a successful identity says nothing about access to other component registers.
func Identify(ctx context.Context, reader ScalarReader, base uint64) (Component, error) {
	if ctx == nil {
		return Component{}, errors.New("coresight: nil context")
	}
	if reader == nil {
		return Component{}, errors.New("coresight: nil scalar reader")
	}
	if base&0xfff != 0 {
		return Component{}, fmt.Errorf("coresight: unaligned identification page %#x", base)
	}
	if err := ctx.Err(); err != nil {
		return Component{}, err
	}
	cidr, err := readID(ctx, reader, base, []uint64{0xff0, 0xff4, 0xff8, 0xffc})
	if err != nil {
		return Component{}, err
	}
	if uint32(cidr)&0xffff0fff != 0xb105000d {
		return Component{}, fmt.Errorf("coresight: invalid CIDR %#08x at %#x", cidr, base)
	}
	pidr, err := readID(ctx, reader, base, []uint64{0xfe0, 0xfe4, 0xfe8, 0xfec, 0xfd0, 0xfd4, 0xfd8, 0xfdc})
	if err != nil {
		return Component{}, err
	}
	c := Component{Base: base, CIDR: uint32(cidr), PIDR: pidr}
	if c.Class() == 9 {
		if err := c.readDevice(ctx, reader); err != nil {
			return Component{}, err
		}
	}
	return c, nil
}

func readID(ctx context.Context, reader ScalarReader, base uint64, offsets []uint64) (uint64, error) {
	var id uint64
	for i, offset := range offsets {
		word, err := readWord(ctx, reader, base+offset)
		if err != nil {
			return 0, err
		}
		id |= uint64(word&0xff) << (8 * i)
	}
	return id, nil
}

func readWord(ctx context.Context, reader ScalarReader, address uint64) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	word, err := reader.ReadScalar(ctx, address, dap.Size32)
	if err != nil {
		return 0, fmt.Errorf("coresight: read %#x: %w", address, err)
	}
	return uint32(word), nil
}

func (c *Component) readDevice(ctx context.Context, reader ScalarReader) error {
	var err error
	c.DEVARCH, err = readWord(ctx, reader, c.Base+0xfbc)
	if err != nil {
		return err
	}
	c.DEVID, err = readWord(ctx, reader, c.Base+0xfc8)
	if err != nil {
		return err
	}
	devtype, err := readWord(ctx, reader, c.Base+0xfcc)
	if err != nil {
		return err
	}
	c.DEVTYPE = uint8(devtype)
	return nil
}
