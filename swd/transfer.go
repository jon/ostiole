package swd

import (
	"context"
	"fmt"
)

// Transfer performs one SWD register transaction without retrying.
func (c *Conn) Transfer(
	ctx context.Context,
	req Request,
	data uint32,
) (uint32, error) {
	if req.Addr&^0x0c != 0 {
		return 0, fmt.Errorf("swd: invalid register address %#02x", req.Addr)
	}
	header := &sequence{}
	header.appendByte(true, requestByte(req))
	header.appendN(c.turnaround+3, false, false)
	input, err := c.exchange(ctx, header)
	if err != nil {
		return 0, err
	}
	ack := readACK(input, 8+c.turnaround)
	if err := ackError(ack); err != nil {
		return 0, c.finishFailed(ctx, err)
	}
	if req.Read {
		return c.readData(ctx)
	}
	return 0, c.writeData(ctx, data)
}

func readACK(input []byte, offset int) byte {
	ack := byte(0)
	for bit := range 3 {
		if bitAt(input, offset+bit) {
			ack |= 1 << uint(bit)
		}
	}
	return ack
}

func ackError(ack byte) error {
	switch ack {
	case 0b001:
		return nil
	case 0b010:
		return ErrWait
	case 0b100:
		return ErrFault
	default:
		return ErrProtocol
	}
}

func (c *Conn) finishFailed(ctx context.Context, ackErr error) error {
	finish := &sequence{}
	finish.appendN(c.turnaround, false, false)
	finish.appendN(c.idleCycles, true, false)
	if _, err := c.exchange(ctx, finish); err != nil {
		return err
	}
	return ackErr
}

func (c *Conn) readData(ctx context.Context) (uint32, error) {
	data := &sequence{}
	data.appendN(33+c.turnaround, false, false)
	data.appendN(c.idleCycles, true, false)
	input, err := c.exchange(ctx, data)
	if err != nil {
		return 0, err
	}
	value := extractUint32(input)
	if bitAt(input, 32) != parity32(value) {
		return 0, ErrParity
	}
	return value, nil
}

func (c *Conn) writeData(ctx context.Context, value uint32) error {
	data := &sequence{}
	data.appendN(c.turnaround, false, false)
	for bit := range 32 {
		data.append(true, value>>uint(bit)&1 != 0)
	}
	data.append(true, parity32(value))
	data.appendN(c.idleCycles, true, false)
	_, err := c.exchange(ctx, data)
	return err
}

func requestByte(req Request) byte {
	fields := req.Addr
	if req.AP {
		fields |= 1
	}
	if req.Read {
		fields |= 2
	}
	header := byte(0x81) | fields<<1
	if parity32(uint32(fields)) {
		header |= 1 << 5
	}
	return header
}

func bitAt(buf []byte, bit int) bool {
	return buf[bit/8]>>(uint(bit)%8)&1 != 0
}

func parity32(value uint32) bool {
	parity := false
	for value != 0 {
		parity = parity != (value&1 != 0)
		value >>= 1
	}
	return parity
}

func extractUint32(buf []byte) uint32 {
	var value uint32
	for bit := range 32 {
		if bitAt(buf, bit) {
			value |= 1 << uint(bit)
		}
	}
	return value
}
