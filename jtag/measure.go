package jtag

import (
	"context"
	"errors"
)

// MeasureIR measures the total instruction-chain length without guessing its
// per-TAP boundaries. maxBits must be in [2, 65536] and bound the actual chain.
// It flushes ones, shifts a zero marker through, then updates BYPASS and ends
// in Idle. If the bound is wrong or the wire fails, the resulting instructions
// cannot be guaranteed. Reset must have established state before this call.
func (c *Conn) MeasureIR(ctx context.Context, maxBits int) (int, error) {
	if _, err := c.validate(ctx); err != nil {
		return 0, err
	}
	if maxBits < 2 || maxBits > 65536 {
		return 0, errors.New("jtag: invalid IR measurement bound")
	}
	if err := c.Move(ctx, Idle); err != nil {
		return 0, err
	}
	if err := c.Move(ctx, ShiftIR); err != nil {
		return 0, err
	}
	if _, err := c.clock(ctx, make([]byte, (maxBits+7)/8), ones(maxBits), maxBits); err != nil {
		return 0, err
	}
	input, tms := ones(maxBits+1), make([]byte, (maxBits+8)/8)
	input[0] &^= 1
	put(tms, maxBits, 1)
	output, err := c.clock(ctx, tms, input, maxBits+1)
	if err != nil {
		return 0, err
	}
	if err := c.Move(ctx, Idle); err != nil {
		return 0, err
	}
	return markerLength(output, maxBits)
}

func markerLength(output []byte, maxBits int) (int, error) {
	length := 0
	for i := 0; i <= maxBits; i++ {
		if get(output, i) == 0 {
			if length != 0 || i < 2 {
				return 0, errors.New("jtag: invalid IR marker response")
			}
			length = i
		}
	}
	if length == 0 {
		return 0, errors.New("jtag: IR marker did not return within bound")
	}
	return length, nil
}

func ones(bits int) []byte {
	data := make([]byte, (bits+7)/8)
	for i := range data {
		data[i] = 0xff
	}
	return data
}
