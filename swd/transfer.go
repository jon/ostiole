package swd

import (
	"context"
	"errors"
	"fmt"
)

// ReadDP reads one debug-port register without retrying. Connect must have
// succeeded, and addr must be 0x00, 0x04, 0x08, or 0x0c.
func (c *Conn) ReadDP(ctx context.Context, addr uint8) (uint32, error) {
	return c.read(ctx, false, addr)
}

// WriteDP writes one debug-port register without retrying. Connect must have
// succeeded, and addr must be 0x00, 0x04, 0x08, or 0x0c. A bank-zero write at
// 0x04 must preserve connection-owned ORUNDETECT. Release restores the
// inherited ORUNDETECT setting but does not restore other CTRL/STAT bits changed
// by the caller.
func (c *Conn) WriteDP(ctx context.Context, addr uint8, value uint32) error {
	return c.write(ctx, false, addr, value)
}

// ReadAP reads one access-port register without retrying. Connect must have
// succeeded, and addr must be 0x00, 0x04, 0x08, or 0x0c.
func (c *Conn) ReadAP(ctx context.Context, addr uint8) (uint32, error) {
	return c.read(ctx, true, addr)
}

// WriteAP writes one access-port register without retrying. Connect must have
// succeeded, and addr must be 0x00, 0x04, 0x08, or 0x0c.
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
	if c.state != connectionReady {
		return 0, errors.New("swd: connection is not ready; call Connect")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := c.validateTransfer(req, value); err != nil {
		return 0, err
	}
	result, err := c.execute(ctx, req, value)
	c.observeTransfer(req, result, value, err)
	return result, err
}

func (c *Conn) execute(ctx context.Context, req request, value uint32) (uint32, error) {
	result, err := c.transferFrame(ctx, req, value)
	if c.response != responseOverrun || err != ErrWait {
		return result, err
	}
	if isAbortWrite(req) {
		c.requireRepair()
		return result, errors.Join(err, errors.New("swd: ABORT returned WAIT; STICKYORUN cleanup is unavailable"))
	}
	if cleanupErr := c.clearOverrunAfterWAIT(ctx); cleanupErr != nil {
		return 0, errors.Join(err, cleanupErr)
	}
	if c.selectPending {
		c.requireRepair()
	}
	return result, err
}

func (c *Conn) clearOverrunAfterWAIT(ctx context.Context) error {
	abort, err := newRequest(false, false, 0x00)
	if err != nil {
		panic(err)
	}
	if _, err := c.transferFrame(ctx, abort, 1<<4); err != nil {
		c.requireRepair()
		return fmt.Errorf("swd: clear STICKYORUN after WAIT: %w", err)
	}
	return nil
}

func (c *Conn) transferFrame(ctx context.Context, req request, value uint32) (uint32, error) {
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

func (c *Conn) validateTransfer(req request, value uint32) error {
	if req.isAP() || req.isRead() || req.address() != 0x04 {
		return nil
	}
	if c.selectPending || !c.bank.valid {
		return errors.New("swd: DP bank is unknown before write at address 0x04")
	}
	if c.bank.bank == 0 {
		enabled := value&1 != 0
		if enabled != (c.response == responseOverrun) {
			return errors.New("swd: CTRL/STAT.ORUNDETECT is owned by the connection")
		}
	}
	if c.bank.bank == 1 && value&(3<<8) != 0 {
		return errors.New("swd: DLCR turnaround changes are not supported")
	}
	return nil
}

func (c *Conn) observeTransfer(req request, result, data uint32, err error) {
	if !completeTransfer(err) {
		c.requireRepair()
		return
	}
	if c.selectPending {
		c.observePendingSelection(req, result, err)
	}
	if !req.isAP() && !req.isRead() && req.address() == 0x08 && err == nil {
		c.priorBank = c.bank
		c.bank = bankSelection{bank: uint8(data & 0x0f), valid: true}
		c.selectPending = true
	}
}

func (c *Conn) observePendingSelection(req request, result uint32, err error) {
	switch {
	case isAbortWrite(req):
		if err == nil {
			c.invalidateBank()
		}
	case isDPIDRRead(req):
	case isBankZeroCTRLSTATRead(req, c.bank, c.priorBank):
		c.resolveBankZeroCTRLSTAT(result, err)
	case err == nil || err == ErrWait || err == ErrParity:
		c.confirmBank()
	}
}

func (c *Conn) resolveBankZeroCTRLSTAT(result uint32, err error) {
	if err != nil {
		return
	}
	if result&writeDataErr != 0 {
		c.invalidateBank()
		return
	}
	c.confirmBank()
}

func isAbortWrite(req request) bool {
	return !req.isAP() && !req.isRead() && req.address() == 0x00
}

func isDPIDRRead(req request) bool {
	return !req.isAP() && req.isRead() && req.address() == 0x00
}

func isBankZeroCTRLSTATRead(req request, bank, prior bankSelection) bool {
	return !req.isAP() && req.isRead() && req.address() == 0x04 && bank.valid && bank.bank == 0 && prior.valid && prior.bank == 0
}

func completeTransfer(err error) bool {
	return err == nil || err == ErrWait || err == ErrFault || err == ErrParity
}

func (c *Conn) confirmBank() {
	c.priorBank = bankSelection{}
	c.selectPending = false
}

func (c *Conn) invalidateBank() {
	c.bank = bankSelection{}
	c.confirmBank()
}

func (c *Conn) requireRepair() {
	if c == nil || c.state == connectionIdle {
		return
	}
	c.state = connectionRepair
	c.invalidateBank()
}

func (c *Conn) transferOverrun(ctx context.Context, req request, value uint32) (uint32, error) {
	frame := c.fixedFrame(req, value)
	input, err := c.exchange(ctx, frame)
	if err != nil {
		return 0, err
	}
	return decodeFixedFrame(input, 0, req)
}

func (c *Conn) fixedFrame(req request, value uint32) *sequence {
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
	return frame
}

func decodeFixedFrame(input []byte, offset int, req request) (uint32, error) {
	if err := ackError(readACK(input, offset+9)); err != nil {
		return 0, err
	}
	if !req.isRead() {
		return 0, nil
	}
	dataOffset := offset + 12
	data := extractUint32At(input, dataOffset)
	if bitAt(input, dataOffset+32) != parity32(data) {
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
	if ackErr == ErrProtocol {
		finish.appendN(33+c.turnaround, false, false)
	} else {
		finish.appendN(c.turnaround, false, false)
	}
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
