// Package sim provides a behavioral Serial Wire Debug model.
package sim

import (
	"context"
	"errors"
	"fmt"
)

// Wire validates SWD traffic without physical hardware.
type Wire struct{}

// SWDIO implements swd.Wire.
func (w *Wire) SWDIO(
	ctx context.Context,
	direction, output []byte,
	bits int,
) ([]byte, error) {
	if w == nil {
		return nil, errors.New("swd/sim: nil wire")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bits < 0 {
		return nil, fmt.Errorf("swd/sim: negative bit count %d", bits)
	}
	need := (bits + 7) / 8
	if len(direction) < need || len(output) < need {
		return nil, fmt.Errorf(
			"swd/sim: direction or output is too short for %d bits",
			bits,
		)
	}
	if bits == 0 {
		return nil, nil
	}
	if lineReset(direction, output, bits) ||
		jtagToSWD(direction, output, bits) {
		return make([]byte, need), nil
	}
	return nil, errors.New("swd/sim: unrecognized wire sequence")
}

func lineReset(direction, output []byte, bits int) bool {
	return bits == 64 &&
		allBits(direction, 0, bits, true) &&
		allBits(output, 0, 56, true) &&
		allBits(output, 56, 8, false)
}

func jtagToSWD(direction, output []byte, bits int) bool {
	return bits == 136 &&
		allBits(direction, 0, bits, true) &&
		allBits(output, 0, 56, true) &&
		byteAt(output, 56) == 0x9e &&
		byteAt(output, 64) == 0xe7 &&
		allBits(output, 72, 56, true) &&
		allBits(output, 128, 8, false)
}

func allBits(buf []byte, offset, count int, want bool) bool {
	for bit := range count {
		if bitAt(buf, offset+bit) != want {
			return false
		}
	}
	return true
}

func byteAt(buf []byte, offset int) byte {
	var value byte
	for bit := range 8 {
		if bitAt(buf, offset+bit) {
			value |= 1 << uint(bit)
		}
	}
	return value
}

func bitAt(buf []byte, bit int) bool {
	return buf[bit/8]>>(uint(bit)%8)&1 != 0
}
