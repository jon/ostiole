package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/usb"
)

const (
	requestReset      = 0x00
	requestSetLatency = 0x09
	requestSetBitmode = 0x0b

	cmdClockBitsOutNegLSB = 0x1b
	cmdClockBitsInPosLSB  = 0x2a
	cmdSetDataLow         = 0x80
	cmdSetDataHigh        = 0x82
	cmdDisableLoop        = 0x85
	cmdSetClockDiv        = 0x86
	cmdSendImmediate      = 0x87
	cmdDisableDivBy5      = 0x8a
	cmdDisable3Phase      = 0x8d
	cmdDisableAdaptive    = 0x97

	pinClock   = 1 << 0
	pinDataOut = 1 << 1

	transferTimeout = 5 * time.Second
)

type device struct {
	raw     *ftdi.Channel
	claimed bool
}

type readChunk struct {
	offset int
	bits   int
}

type frame struct {
	dir, out []byte
	bits     int
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()
	dpidr, err := readDPIDR(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("DPIDR=%#08x\n", dpidr)
}

func readDPIDR(ctx context.Context) (value uint32, err error) {
	enumerator := usb.New()
	attachments, err := enumerator.List(ctx, []usb.DeviceFilter{
		{VID: ftdi.VID, PID: ftdi.PIDFT2232H},
		{VID: ftdi.VID, PID: ftdi.PIDFT4232H},
		{VID: ftdi.VID, PID: ftdi.PIDFT232H},
	})
	if err != nil {
		return 0, err
	}
	if len(attachments) != 1 {
		return 0, fmt.Errorf(
			"ostiole: require exactly one supported FTDI attachment; found %d",
			len(attachments),
		)
	}
	opened, err := enumerator.Open(ctx, attachments[0])
	if err != nil {
		return 0, err
	}
	channel, err := ftdi.NewChannel(opened, ftdi.Config{
		ProductID: attachments[0].PID,
		Port:      ftdi.PortA,
		Interface: ftdi.SWD,
	})
	if err != nil {
		return 0, errors.Join(err, opened.Close())
	}
	raw := &device{raw: channel}
	defer func() {
		err = errors.Join(err, raw.close())
	}()
	if err := raw.enterMPSSE(ctx); err != nil {
		return 0, err
	}
	if err := raw.jtagToSWD(ctx); err != nil {
		return 0, err
	}
	value, err = raw.transferRead(ctx, 0xa5)
	if err != nil {
		return 0, err
	}
	if value == 0 || value == math.MaxUint32 || value&1 == 0 {
		return 0, fmt.Errorf("ostiole: invalid DPIDR %#08x", value)
	}
	return value, nil
}

func (d *device) enterMPSSE(ctx context.Context) error {
	if err := d.raw.Claim(); err != nil {
		return fmt.Errorf("ostiole: claim USB interface: %w", err)
	}
	d.claimed = true
	steps := [][2]uint16{
		{requestReset, 0},
		{requestReset, 1},
		{requestReset, 2},
		{requestSetLatency, 2},
		{requestSetBitmode, 0},
		{requestSetBitmode, 0x0200},
	}
	for _, step := range steps {
		if err := d.control(ctx, uint8(step[0]), step[1]); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(50 * time.Millisecond):
	}
	if err := d.synchronize(ctx); err != nil {
		return err
	}
	divisor := uint16(60_000_000/(2*400_000) - 1)
	commands := []byte{
		cmdDisableDivBy5,
		cmdDisableAdaptive,
		cmdDisable3Phase,
		cmdSetClockDiv, byte(divisor), byte(divisor >> 8),
		cmdDisableLoop,
		cmdSetDataLow, 0, pinClock,
		cmdSetDataHigh, 0, 0,
	}
	return d.raw.WriteExact(ctx, commands)
}

func (d *device) synchronize(ctx context.Context) error {
	if err := d.raw.WriteExact(ctx, []byte{0xab}); err != nil {
		return err
	}
	payload, err := d.raw.ReadPayload(ctx, 2)
	if err != nil {
		return err
	}
	if len(payload) != 2 || payload[0] != 0xfa || payload[1] != 0xab {
		return fmt.Errorf("ostiole: unexpected MPSSE synchronization %x", payload)
	}
	return nil
}

func (d *device) close() error {
	if d.raw == nil {
		return nil
	}
	var result error
	if d.claimed {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for _, step := range [][2]uint16{
			{requestSetBitmode, 0},
			{requestSetLatency, 16},
			{requestReset, 1},
			{requestReset, 2},
		} {
			result = errors.Join(
				result,
				d.control(ctx, uint8(step[0]), step[1]),
			)
		}
		result = errors.Join(result, d.raw.Release())
	}
	return errors.Join(result, d.raw.Close())
}

func (d *device) control(
	ctx context.Context,
	request uint8,
	value uint16,
) error {
	_, err := d.raw.Control(ctx, request, value)
	if err != nil {
		return fmt.Errorf(
			"ostiole: FTDI request %#02x value %#04x: %w",
			request,
			value,
			err,
		)
	}
	return nil
}

func (d *device) jtagToSWD(ctx context.Context) error {
	wire := &frame{}
	wire.pushN(56, true, true)
	wire.pushByte(true, 0x9e)
	wire.pushByte(true, 0xe7)
	wire.pushN(56, true, true)
	wire.pushN(8, true, false)
	_, err := d.swdIO(ctx, wire.dir, wire.out, wire.bits)
	return err
}

func (d *device) transferRead(
	ctx context.Context,
	request byte,
) (uint32, error) {
	first := &frame{}
	first.pushByte(true, request)
	first.pushN(4, false, false)
	input, err := d.swdIO(ctx, first.dir, first.out, first.bits)
	if err != nil {
		return 0, err
	}
	ack := byte(0)
	for bit := range 3 {
		if bitAt(input, 9+bit) {
			ack |= 1 << uint(bit)
		}
	}
	if ack != 0b001 {
		return 0, fmt.Errorf("ostiole: SWD ACK=%03b", ack)
	}
	second := &frame{}
	second.pushN(33, false, false)
	second.pushN(1, false, false)
	second.pushN(8, true, false)
	input, err = d.swdIO(ctx, second.dir, second.out, second.bits)
	if err != nil {
		return 0, err
	}
	value := extractUint32(input)
	if bitAt(input, 32) != parity32(value) {
		return 0, errors.New("ostiole: SWD read parity error")
	}
	return value, nil
}

func (d *device) swdIO(
	ctx context.Context,
	direction, output []byte,
	bits int,
) ([]byte, error) {
	commands, reads := swdCommands(direction, output, bits)
	if err := d.raw.WriteExact(ctx, commands); err != nil {
		return nil, err
	}
	var response []byte
	if len(reads) != 0 {
		var err error
		response, err = d.raw.ReadPayload(ctx, len(reads))
		if err != nil {
			return nil, err
		}
	}
	return decodeSamples(response, reads, bits), nil
}

func swdCommands(
	direction, output []byte,
	bits int,
) ([]byte, []readChunk) {
	commands := make([]byte, 0, bits)
	var reads []readChunk
	for offset := 0; offset < bits; {
		hostDrives := bitAt(direction, offset)
		width := runWidth(direction, offset, bits, hostDrives)
		if hostDrives {
			data := runData(output, offset, width)
			commands = append(
				commands,
				cmdSetDataLow, (data&1)<<1, pinClock|pinDataOut,
				cmdClockBitsOutNegLSB, byte(width-1), data,
			)
		} else {
			commands = append(
				commands,
				cmdSetDataLow, 0, pinClock,
				cmdClockBitsInPosLSB, byte(width-1),
			)
			reads = append(reads, readChunk{offset: offset, bits: width})
		}
		offset += width
	}
	commands = append(commands, cmdSetDataLow, 0, pinClock)
	if len(reads) != 0 {
		commands = append(commands, cmdSendImmediate)
	}
	return commands, reads
}

func runWidth(
	direction []byte,
	offset, bits int,
	hostDrives bool,
) int {
	width := 1
	for width < 8 &&
		offset+width < bits &&
		bitAt(direction, offset+width) == hostDrives {
		width++
	}
	return width
}

func runData(output []byte, offset, bits int) byte {
	var data byte
	for bit := range bits {
		if bitAt(output, offset+bit) {
			data |= 1 << uint(bit)
		}
	}
	return data
}

func decodeSamples(
	response []byte,
	reads []readChunk,
	bits int,
) []byte {
	input := make([]byte, (bits+7)/8)
	for index, read := range reads {
		samples := response[index] >> uint(8-read.bits)
		for bit := range read.bits {
			if samples>>uint(bit)&1 != 0 {
				setBit(input, read.offset+bit, true)
			}
		}
	}
	return input
}

func (f *frame) push(driven, value bool) {
	if f.bits%8 == 0 {
		f.dir = append(f.dir, 0)
		f.out = append(f.out, 0)
	}
	setBit(f.dir, f.bits, driven)
	setBit(f.out, f.bits, value)
	f.bits++
}

func (f *frame) pushN(bits int, driven, value bool) {
	for range bits {
		f.push(driven, value)
	}
}

func (f *frame) pushByte(driven bool, value byte) {
	for bit := range 8 {
		f.push(driven, value>>uint(bit)&1 != 0)
	}
}

func bitAt(buffer []byte, bit int) bool {
	return buffer[bit/8]>>(uint(bit)%8)&1 != 0
}

func setBit(buffer []byte, bit int, value bool) {
	if value {
		buffer[bit/8] |= 1 << (uint(bit) % 8)
	}
}

func extractUint32(buffer []byte) uint32 {
	var value uint32
	for bit := range 32 {
		if bitAt(buffer, bit) {
			value |= 1 << uint(bit)
		}
	}
	return value
}

func parity32(value uint32) bool {
	parity := false
	for bit := range 32 {
		if value>>uint(bit)&1 != 0 {
			parity = !parity
		}
	}
	return parity
}
