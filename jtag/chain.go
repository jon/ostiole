package jtag

import (
	"context"
	"errors"
	"fmt"
)

// ErrChainInvalid requires Connect after a failed operation, release, or raw
// use of the underlying connection. Old borrowed TAPs never become valid again.
var ErrChainInvalid = errors.New("jtag: chain requires validation")

// Chain owns an explicit layout and exclusive use of a Conn. It does not own
// the wire's resources. All calls, including borrowed TAP calls, must be serialized.
type Chain struct {
	conn               *Conn
	layout             Layout
	irBits             int
	serial, generation uint64
	valid              bool
	released           bool
	selected           int
}

// NewChain copies an explicit layout without traffic. Connect performs validation.
func NewChain(conn *Conn, layout Layout) (*Chain, error) {
	if conn == nil || len(layout) == 0 || len(layout) > 1024 {
		return nil, errors.New("jtag: connection and bounded nonempty layout required")
	}
	c := &Chain{conn: conn, layout: append(Layout(nil), layout...), selected: -1}
	for _, spec := range c.layout {
		if spec.ir < 2 || spec.ir > 64 {
			return nil, errors.New("jtag: invalid TAP specification")
		}
		c.irBits += spec.ir
	}
	return c, nil
}

// Connect resets and checks the complete reset chain and each supplied IR
// capture boundary, then parks all TAPs in BYPASS/Idle. Every attempt invalidates
// earlier borrowed TAPs. It never changes board-specific chain routing.
func (c *Chain) Connect(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return ErrChainInvalid
	}
	c.valid, c.selected = false, -1
	c.released = false
	c.generation++
	found, err := c.conn.Discover(ctx, len(c.layout))
	if err != nil {
		return err
	}
	if len(found) != len(c.layout) {
		return errors.New("jtag: TAP count mismatch")
	}
	for i, spec := range c.layout {
		if found[i] != spec.reset {
			return fmt.Errorf("jtag: reset register mismatch at TAP %d", i)
		}
	}
	length, err := c.conn.MeasureIR(ctx, 65536)
	if err != nil {
		return err
	}
	if length != c.irBits {
		return errors.New("jtag: total IR length mismatch")
	}
	data := ones(c.irBits + 32)
	capture, err := c.conn.ScanIR(ctx, data, c.irBits+32)
	if err != nil {
		return err
	}
	if err := c.checkIR(capture); err != nil {
		return err
	}
	c.serial, c.valid = c.conn.serial, true
	return nil
}

func (c *Chain) checkIR(capture []byte) error {
	offset := 0
	for i, spec := range c.layout {
		if get(capture, offset) != 1 || get(capture, offset+1) != 0 {
			return fmt.Errorf("jtag: IR capture mismatch at TAP %d", i)
		}
		offset += spec.ir
	}
	if word(capture, offset) != ^uint32(0) {
		return errors.New("jtag: IR chain exceeds supplied layout")
	}
	return nil
}

func (c *Chain) ready() bool {
	return c != nil && c.conn != nil && c.valid && c.serial == c.conn.serial
}

// Release parks a validated chain in BYPASS/Idle and invalidates all borrowed
// TAPs. It does not restore inherited instructions or close the wire. If state
// was lost, it revalidates the same layout before parking; failure is retryable.
func (c *Chain) Release(ctx context.Context) error {
	if c == nil || c.released {
		return nil
	}
	if !c.ready() {
		if err := c.Connect(ctx); err != nil {
			return err
		}
	}
	c.valid = false
	c.generation++
	_, err := c.conn.ScanIR(ctx, ones(c.irBits), c.irBits)
	c.released = err == nil
	return err
}
