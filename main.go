package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"github.com/jon/ostiole/usb"
)

const (
	ftdiVID = 0x0403

	ft2232hPID = 0x6010
	ft4232hPID = 0x6011
	ft232hPID  = 0x6014

	interfaceA = 0
	indexA     = 1
	bulkOutA   = 0x02
	bulkInA    = 0x81

	usbfsClaimInterface   = 0x8004550f
	usbfsControl          = 0xc0185500
	usbfsBulk             = 0xc0185502
	usbfsReleaseInterface = 0x80045510

	requestReset      = 0x00
	requestSetLatency = 0x09
	requestSetBitmode = 0x0b
	requestTypeOut    = 0x40

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

type usbControlTransfer struct {
	RequestType uint8
	Request     uint8
	Value       uint16
	Index       uint16
	Length      uint16
	Timeout     uint32
	Data        uintptr
}

type usbBulkTransfer struct {
	Endpoint uint32
	Length   uint32
	Timeout  uint32
	Data     uintptr
}

type device struct {
	file    *os.File
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
		{VID: ftdiVID, PID: ft2232hPID},
		{VID: ftdiVID, PID: ft4232hPID},
		{VID: ftdiVID, PID: ft232hPID},
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
	raw, err := openAttachment("/dev/bus/usb", attachments[0])
	if err != nil {
		return 0, err
	}
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

func openAttachment(root string, item usb.DeviceInfo) (*device, error) {
	path := filepath.Join(
		root,
		fmt.Sprintf("%03d", item.Bus),
		fmt.Sprintf("%03d", item.Address),
	)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("ostiole: open USB attachment: %w", err)
	}
	return &device{file: file}, nil
}

func (d *device) enterMPSSE(ctx context.Context) error {
	if err := d.interfaceIOCTL(usbfsClaimInterface); err != nil {
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
		if err := d.control(ctx, uint8(step[0]), step[1], indexA); err != nil {
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
	return d.bulkWrite(ctx, commands)
}

func (d *device) synchronize(ctx context.Context) error {
	if err := d.bulkWrite(ctx, []byte{0xab}); err != nil {
		return err
	}
	payload, err := d.bulkRead(ctx, 2)
	if err != nil {
		return err
	}
	if len(payload) != 2 || payload[0] != 0xfa || payload[1] != 0xab {
		return fmt.Errorf("ostiole: unexpected MPSSE synchronization %x", payload)
	}
	return nil
}

func (d *device) close() error {
	if d.file == nil {
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
				d.control(ctx, uint8(step[0]), step[1], indexA),
			)
		}
		result = errors.Join(result, d.interfaceIOCTL(usbfsReleaseInterface))
	}
	return errors.Join(result, d.file.Close())
}

func (d *device) control(
	ctx context.Context,
	request uint8,
	value, index uint16,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	transfer := usbControlTransfer{
		RequestType: requestTypeOut,
		Request:     request,
		Value:       value,
		Index:       index,
		Timeout:     uint32(transferTimeout / time.Millisecond),
	}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		d.file.Fd(),
		usbfsControl,
		uintptr(unsafe.Pointer(&transfer)),
	)
	if errno != 0 {
		return fmt.Errorf(
			"ostiole: FTDI request %#02x value %#04x: %w",
			request,
			value,
			errno,
		)
	}
	return nil
}

func (d *device) interfaceIOCTL(request uintptr) error {
	value := uint32(interfaceA)
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		d.file.Fd(),
		request,
		uintptr(unsafe.Pointer(&value)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func (d *device) bulkWrite(ctx context.Context, data []byte) error {
	count, err := d.bulkTransfer(ctx, bulkOutA, data)
	if err != nil {
		return err
	}
	if count != len(data) {
		return fmt.Errorf("ostiole: short USB write %d/%d", count, len(data))
	}
	return nil
}

func (d *device) bulkRead(ctx context.Context, payloadBytes int) ([]byte, error) {
	payload := make([]byte, 0, payloadBytes)
	for len(payload) < payloadBytes {
		raw := make([]byte, payloadBytes-len(payload)+2)
		count, err := d.bulkTransfer(ctx, bulkInA, raw)
		if err != nil {
			return nil, err
		}
		payload, err = appendFTDIPacket(payload, raw, count)
		if err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func appendFTDIPacket(payload, packet []byte, count int) ([]byte, error) {
	if count < 2 {
		return nil, fmt.Errorf("ostiole: short FTDI status packet: %d", count)
	}
	return append(payload, packet[2:count]...), nil
}

func (d *device) bulkTransfer(
	ctx context.Context,
	endpoint uint8,
	data []byte,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	transfer := usbBulkTransfer{
		Endpoint: uint32(endpoint),
		Length:   uint32(len(data)),
		Timeout:  uint32(transferTimeout / time.Millisecond),
	}
	if len(data) != 0 {
		transfer.Data = uintptr(unsafe.Pointer(&data[0]))
	}
	count, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		d.file.Fd(),
		usbfsBulk,
		uintptr(unsafe.Pointer(&transfer)),
	)
	if errno != 0 {
		return 0, fmt.Errorf("ostiole: USB bulk endpoint %#02x: %w", endpoint, errno)
	}
	return int(count), nil
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
	if err := d.bulkWrite(ctx, commands); err != nil {
		return nil, err
	}
	var response []byte
	if len(reads) != 0 {
		var err error
		response, err = d.bulkRead(ctx, len(reads))
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
