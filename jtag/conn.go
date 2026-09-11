package jtag

import (
	"context"
	"errors"
)

// Wire clocks exactly bits TCK cycles, driving TMS and TDI and sampling TDO.
// Buffers pack the earliest bit in bit zero of byte zero. Implementations
// return ceil(bits/8) bytes and do not retain or modify the input buffers.
// An error can mean any prefix was clocked. Typed nil implementations are invalid.
type Wire interface {
	JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error)
}

// TransferLimits optionally bounds one wire call. The limit must be positive.
type TransferLimits interface{ MaxTransferBits() int }

// ErrStateUnknown requires an explicit successful Reset before further movement.
var ErrStateUnknown = errors.New("jtag: TAP state is unknown; reset required")

// Conn owns TAP state, not the wire's resources. Give it exclusive use of the
// wire and serialize all calls. A zero Conn has no wire. Movement may update
// instructions or data registers; even Reset can affect target debug state.
type Conn struct {
	wire   Wire
	state  State
	serial uint64
}

// New constructs a connection without traffic. Reset establishes its TAP state.
func New(wire Wire) *Conn { return &Conn{wire: wire} }

// State returns the last confirmed state, or Unknown after an ambiguous error.
func (c *Conn) State() State {
	if c == nil {
		return Unknown
	}
	return c.state
}

func (c *Conn) validate(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, errors.New("jtag: nil context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if c == nil || c.wire == nil {
		return 0, errors.New("jtag: nil wire")
	}
	limit := 4096
	if w, ok := c.wire.(TransferLimits); ok {
		if w.MaxTransferBits() <= 0 {
			return 0, errors.New("jtag: invalid wire transfer limit")
		}
		limit = min(limit, w.MaxTransferBits())
	}
	return limit, nil
}

// Reset clocks five TMS-high cycles without asserting a physical reset pin.
// It is the only movement available while TAP state is unknown.
func (c *Conn) Reset(ctx context.Context) error {
	if _, err := c.validate(ctx); err != nil {
		return err
	}
	if _, err := c.clock(ctx, []byte{31}, []byte{0}, 5); err != nil {
		return err
	}
	c.state = Reset
	return nil
}

// Move takes a shortest TMS path to a known destination. It drives TDI low;
// paths through shift or update states have their ordinary target effects.
// No clocks are sent when already at the destination.
func (c *Conn) Move(ctx context.Context, target State) error {
	if _, err := c.validate(ctx); err != nil {
		return err
	}
	if target < Reset || target > UpdateIR {
		return errors.New("jtag: invalid destination")
	}
	if c.state == Unknown {
		return ErrStateUnknown
	}
	bits := path(c.state, target)
	tms := make([]byte, (len(bits)+7)/8)
	for i, bit := range bits {
		if bit {
			put(tms, i, 1)
		}
	}
	_, err := c.clock(ctx, tms, make([]byte, len(tms)), len(bits))
	return err
}

func get(data []byte, bit int) byte        { return data[bit/8] >> uint(bit%8) & 1 }
func put(data []byte, bit int, value byte) { data[bit/8] |= value << uint(bit%8) }

func (c *Conn) clock(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	limit, err := c.validate(ctx)
	if err != nil {
		return nil, err
	}
	output := make([]byte, (bits+7)/8)
	for start := 0; start < bits; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := min(limit, bits-start)
		ms, di := make([]byte, (n+7)/8), make([]byte, (n+7)/8)
		for i := range n {
			put(ms, i, get(tms, start+i))
			put(di, i, get(tdi, start+i))
		}
		do, err := c.wire.JTAGIO(ctx, ms, di, n)
		c.serial++
		if err == nil && len(do) != len(di) {
			err = errors.New("jtag: invalid wire response length")
		}
		if err != nil {
			c.state = Unknown
			return nil, err
		}
		for i := range n {
			put(output, start+i, get(do, i))
			if c.state != Unknown {
				c.state = transitions[c.state][get(ms, i)]
			}
		}
		start += n
	}
	return output, nil
}
