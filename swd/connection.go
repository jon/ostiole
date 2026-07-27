package swd

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrWait reports an SWD WAIT acknowledgement.
	ErrWait = errors.New("swd: ACK WAIT")
	// ErrFault reports an SWD FAULT acknowledgement.
	ErrFault = errors.New("swd: ACK FAULT")
	// ErrParity reports invalid read-data parity.
	ErrParity = errors.New("swd: read data parity mismatch")
	// ErrProtocol reports an acknowledgement outside the defined values.
	ErrProtocol = errors.New("swd: invalid ACK")
)

// Wire clocks direction-explicit SWD bits.
type Wire interface {
	SWDIO(context.Context, []byte, []byte, int) ([]byte, error)
}

// Request describes one SWD register transaction.
type Request struct {
	AP   bool
	Read bool
	Addr uint8
}

// Conn performs SWD transactions over one wire owner.
type Conn struct {
	wire       Wire
	turnaround int
	idleCycles int
}

// New returns an SWD connection using wire.
func New(w Wire) *Conn {
	return &Conn{wire: w, turnaround: 1, idleCycles: 8}
}

func (c *Conn) exchange(
	ctx context.Context,
	seq *Sequence,
) ([]byte, error) {
	if c == nil || c.wire == nil {
		return nil, errors.New("swd: nil wire")
	}
	input, err := c.wire.SWDIO(
		ctx,
		seq.Direction(),
		seq.Output(),
		seq.Bits(),
	)
	if err != nil {
		return nil, err
	}
	need := (seq.Bits() + 7) / 8
	if len(input) < need {
		return nil, fmt.Errorf("swd: wire returned %d bytes for %d bits; want %d",
			len(input), seq.Bits(), need)
	}
	return input, nil
}
