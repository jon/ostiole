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

// Conn performs one serialized stream of SWD transactions over a wire. Its
// methods are not safe for concurrent use.
type Conn struct {
	wire       Wire
	turnaround int
	idleCycles int
}

// New returns an SWD connection using wire. The caller must serialize all
// calls. A higher-level client that caches protocol state requires exclusive
// use of the connection.
func New(w Wire) *Conn {
	return &Conn{wire: w, turnaround: 1, idleCycles: 8}
}

func (c *Conn) exchange(ctx context.Context, seq *sequence) ([]byte, error) {
	if c == nil || c.wire == nil {
		return nil, errors.New("swd: nil wire")
	}
	input, err := c.wire.SWDIO(ctx, seq.direction, seq.output, seq.bits)
	if err != nil {
		return nil, err
	}
	need := (seq.bits + 7) / 8
	if len(input) < need {
		return nil, fmt.Errorf("swd: wire returned %d bytes for %d bits; want %d",
			len(input), seq.bits, need)
	}
	return input, nil
}
