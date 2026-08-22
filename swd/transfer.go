package swd

import (
	"context"
	"errors"
)

// ReadDP reads one debug-port register without retrying. addr must be 0x00,
// 0x04, 0x08, or 0x0c.
func (c *Conn) ReadDP(ctx context.Context, addr uint8) (uint32, error) {
	return c.read(ctx, false, addr)
}

// WriteDP writes one debug-port register without retrying. addr must be 0x00,
// 0x04, 0x08, or 0x0c.
func (c *Conn) WriteDP(ctx context.Context, addr uint8, value uint32) error {
	return c.write(ctx, false, addr, value)
}

// ReadAP reads one access-port register without retrying. addr must be 0x00,
// 0x04, 0x08, or 0x0c.
func (c *Conn) ReadAP(ctx context.Context, addr uint8) (uint32, error) {
	return c.read(ctx, true, addr)
}

// WriteAP writes one access-port register without retrying. addr must be 0x00,
// 0x04, 0x08, or 0x0c.
func (c *Conn) WriteAP(ctx context.Context, addr uint8, value uint32) error {
	return c.write(ctx, true, addr, value)
}

func (c *Conn) read(ctx context.Context, ap bool, addr uint8) (uint32, error) {
	req, err := newRequest(ap, true, addr)
	if err != nil {
		return 0, err
	}
	return c.transfer(ctx, req, 0)
}

func (c *Conn) write(ctx context.Context, ap bool, addr uint8, value uint32) error {
	req, err := newRequest(ap, false, addr)
	if err != nil {
		return err
	}
	_, err = c.transfer(ctx, req, value)
	return err
}

func (c *Conn) transfer(ctx context.Context, req request, value uint32) (uint32, error) {
	if c == nil {
		return 0, errors.New("swd: nil connection")
	}
	if c.response == responseOverrun {
		return c.transferOverrun(ctx, req, value)
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
	if req.isRead() {
		return c.readData(ctx)
	}
	return 0, c.writeData(ctx, value)
}

func (c *Conn) transferOverrun(ctx context.Context, req request, value uint32) (uint32, error) {
	frame := &sequence{}
	frame.appendByte(true, requestByte(req))
	frame.appendN(c.turnaround+3, false, false)
	if req.isRead() {
		frame.appendN(33+c.turnaround, false, false)
	} else {
		frame.appendN(c.turnaround, false, false)
		for bit := range 32 {
			frame.append(true, value>>uint(bit)&1 != 0)
		}
		frame.append(true, parity32(value))
	}
	frame.appendN(c.idleCycles, true, false)
	input, err := c.exchange(ctx, frame)
	if err != nil {
		return 0, err
	}
	if err := ackError(readACK(input, 8+c.turnaround)); err != nil {
		return 0, err
	}
	if !req.isRead() {
		return 0, nil
	}
	offset := 8 + c.turnaround + 3
	data := extractUint32At(input, offset)
	if bitAt(input, offset+32) != parity32(data) {
		return 0, ErrParity
	}
	return data, nil
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
		return errors.Join(ackErr, err)
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

func requestByte(req request) byte {
	fields := req.address()
	if req.isAP() {
		fields |= 1
	}
	if req.isRead() {
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
	return extractUint32At(buf, 0)
}

func extractUint32At(buf []byte, offset int) uint32 {
	var value uint32
	for bit := range 32 {
		if bitAt(buf, offset+bit) {
			value |= 1 << uint(bit)
		}
	}
	return value
}
