// Package sim provides a behavioral Serial Wire Debug model.
package sim

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

// Request describes a request decoded from the simulated wire. It is supplied
// to Target and Acknowledger and cannot be submitted to swd.Conn.
type Request struct {
	AP   bool
	Read bool
	Addr uint8
}

// Target supplies DP and AP register behavior.
type Target interface {
	Read(context.Context, Request) (uint32, error)
	Write(context.Context, Request, uint32) error
}

// Acknowledger lets a target choose the acknowledgement before a request's
// data phase. Returning swd.ErrWait or swd.ErrFault emits that acknowledgement
// without executing the target read or write.
type Acknowledger interface {
	Acknowledge(context.Context, Request) error
}

// Wire validates SWD traffic without physical hardware.
type Wire struct {
	target     Target
	active     bool
	pending    *Request
	pendingACK byte
}

// New returns a fresh wire backed by target.
func New(target Target) *Wire {
	return &Wire{target: target}
}

// SWDIO implements swd.Wire.
func (w *Wire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
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
		return nil, fmt.Errorf("swd/sim: direction or output is too short for %d bits", bits)
	}
	if bits == 0 {
		return nil, nil
	}
	if lineReset(direction, output, bits) ||
		jtagToSWD(direction, output, bits) {
		w.active = true
		w.pending = nil
		return make([]byte, need), nil
	}
	if !w.active {
		return nil, errors.New("swd/sim: protocol entry is required")
	}
	if w.pending == nil {
		return w.request(ctx, direction, output, bits)
	}
	return w.data(ctx, direction, output, bits)
}

func (w *Wire) request(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if bits != 12 ||
		!allBits(direction, 0, 8, true) ||
		!allBits(direction, 8, 4, false) {
		return nil, errors.New("swd/sim: invalid request direction")
	}
	req, err := decodeRequest(byteAt(output, 0))
	if err != nil {
		return nil, err
	}
	if w.target == nil {
		return nil, errors.New("swd/sim: no target is configured")
	}
	ack, err := acknowledge(ctx, w.target, req)
	if err != nil {
		return nil, err
	}
	w.pending = &req
	w.pendingACK = ack
	input := make([]byte, 2)
	for bit := range 3 {
		setBit(input, 9+bit, ack>>uint(bit)&1 != 0)
	}
	return input, nil
}

func (w *Wire) data(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	req := *w.pending
	w.pending = nil
	if w.pendingACK != 0b001 {
		return failedACK(direction, output, bits)
	}
	if req.Read {
		return w.read(ctx, direction, bits, req)
	}
	return w.write(ctx, direction, output, bits, req)
}

func acknowledge(ctx context.Context, target Target, req Request) (byte, error) {
	a, ok := target.(Acknowledger)
	if !ok {
		return 0b001, nil
	}
	err := a.Acknowledge(ctx, req)
	switch {
	case err == nil:
		return 0b001, nil
	case errors.Is(err, swd.ErrFault):
		return 0b100, nil
	case errors.Is(err, swd.ErrWait):
		return 0b010, nil
	default:
		return 0, err
	}
}

func failedACK(direction, output []byte, bits int) ([]byte, error) {
	if bits != 9 || bitAt(direction, 0) ||
		!allBits(direction, 1, 8, true) || !allBits(output, 1, 8, false) {
		return nil, errors.New("swd/sim: invalid transfer cleanup after WAIT or FAULT")
	}
	return make([]byte, 2), nil
}

func (w *Wire) read(ctx context.Context, direction []byte, bits int, req Request) ([]byte, error) {
	if bits != 42 ||
		!allBits(direction, 0, 34, false) ||
		!allBits(direction, 34, 8, true) {
		return nil, errors.New("swd/sim: invalid read-data direction")
	}
	value, err := w.target.Read(ctx, req)
	if err != nil {
		return nil, err
	}
	input := make([]byte, 6)
	setUint32(input, value)
	setBit(input, 32, parity32(value))
	return input, nil
}

func (w *Wire) write(ctx context.Context, direction, output []byte, bits int, req Request) ([]byte, error) {
	if bits != 42 ||
		bitAt(direction, 0) ||
		!allBits(direction, 1, 41, true) {
		return nil, errors.New("swd/sim: invalid write-data direction")
	}
	value := uint32At(output, 1)
	if bitAt(output, 33) != parity32(value) {
		return nil, errors.New("swd/sim: invalid write-data parity")
	}
	return make([]byte, 6), w.target.Write(ctx, req, value)
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

func decodeRequest(header byte) (Request, error) {
	fields := header >> 1 & 0x0f
	if header&0xc1 != 0x81 ||
		(header>>5&1 != 0) != parity32(uint32(fields)) {
		return Request{}, fmt.Errorf("swd/sim: invalid request header %#02x", header)
	}
	return Request{
		AP:   fields&1 != 0,
		Read: fields&2 != 0,
		Addr: fields >> 2 << 2,
	}, nil
}

func bitAt(buf []byte, bit int) bool {
	return buf[bit/8]>>(uint(bit)%8)&1 != 0
}

func setBit(buf []byte, bit int, value bool) {
	if value {
		buf[bit/8] |= 1 << (uint(bit) % 8)
	}
}

func setUint32(buf []byte, value uint32) {
	for bit := range 32 {
		setBit(buf, bit, value>>uint(bit)&1 != 0)
	}
}

func uint32At(buf []byte, offset int) uint32 {
	var value uint32
	for bit := range 32 {
		if bitAt(buf, offset+bit) {
			value |= 1 << uint(bit)
		}
	}
	return value
}

func parity32(value uint32) bool {
	value ^= value >> 16
	value ^= value >> 8
	value ^= value >> 4
	value ^= value >> 2
	value ^= value >> 1
	return value&1 != 0
}
