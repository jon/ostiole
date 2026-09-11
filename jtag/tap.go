package jtag

import (
	"context"
	"errors"
)

// ErrInstructionChanged requires ScanIR after another TAP selected an instruction.
var ErrInstructionChanged = errors.New("jtag: selected TAP instruction changed")

// TAP borrows one position in a validated chain. Connect, Release, raw Conn
// traffic, and failed chain operations invalidate it. Do not copy the Chain.
type TAP struct {
	chain      *Chain
	index      int
	generation uint64
}

// TAP lends an explicitly selected zero-based position, nearest TDO first.
func (c *Chain) TAP(index int) (TAP, error) {
	if !c.ready() {
		return TAP{}, ErrChainInvalid
	}
	if index < 0 || index >= len(c.layout) {
		return TAP{}, errors.New("jtag: TAP index out of range")
	}
	return TAP{c, index, c.generation}, nil
}

func (t TAP) valid() bool { return t.chain.ready() && t.generation == t.chain.generation }

// ScanIR selects one instruction and sets every other TAP to BYPASS. The
// buffer must be exactly ceil(IRBits/8) bytes, with unused high bits zero.
// The returned capture contains only this TAP's instruction register.
func (t TAP) ScanIR(ctx context.Context, instruction []byte) ([]byte, error) {
	if !t.valid() {
		return nil, ErrChainInvalid
	}
	c := t.chain
	bits := c.layout[t.index].ir
	if len(instruction) != (bits+7)/8 || bits%8 != 0 && instruction[len(instruction)-1]>>uint(bits%8) != 0 {
		return nil, errors.New("jtag: invalid instruction buffer")
	}
	data := ones(c.irBits)
	offset := 0
	for _, spec := range c.layout[:t.index] {
		offset += spec.ir
	}
	for i := range bits {
		mask := byte(1 << uint((offset+i)%8))
		data[(offset+i)/8] &^= mask
		put(data, offset+i, get(instruction, i))
	}
	capture, err := c.conn.ScanIR(ctx, data, c.irBits)
	if err := c.complete(err); err != nil {
		return nil, err
	}
	c.selected = t.index
	return extract(capture, offset, bits), nil
}

// ScanDR scans this TAP with one bypass bit for every other TAP. ScanIR must
// first select its instruction. The returned data excludes bypass padding.
func (t TAP) ScanDR(ctx context.Context, data []byte, bits int) ([]byte, error) {
	if !t.valid() {
		return nil, ErrChainInvalid
	}
	c := t.chain
	if c.selected != t.index {
		return nil, ErrInstructionChanged
	}
	if bits <= 0 || bits > MaxScanBits-len(c.layout)+1 || len(data) < (bits+7)/8 {
		return nil, errors.New("jtag: invalid selected scan length or buffer")
	}
	total := bits + len(c.layout) - 1
	input := make([]byte, (total+7)/8)
	for i := range bits {
		put(input, t.index+i, get(data, i))
	}
	capture, err := c.conn.ScanDR(ctx, input, total)
	if err := c.complete(err); err != nil {
		return nil, err
	}
	return extract(capture, t.index, bits), nil
}

// Idle supplies execution clocks without changing the selected instruction.
func (t TAP) Idle(ctx context.Context, cycles int) error {
	if !t.valid() {
		return ErrChainInvalid
	}
	return t.chain.complete(t.chain.conn.Idle(ctx, cycles))
}

func extract(data []byte, offset, bits int) []byte {
	result := make([]byte, (bits+7)/8)
	for i := range bits {
		put(result, i, get(data, offset+i))
	}
	return result
}

func (c *Chain) complete(err error) error {
	c.serial = c.conn.serial
	if err != nil {
		c.valid = false
		c.selected = -1
	}
	return err
}
