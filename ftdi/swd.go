package ftdi

import (
	"context"
	"fmt"
)

const (
	cmdClockBitsOutNegLSB = 0x1b
	cmdClockBitsInPosLSB  = 0x2a
	cmdSendImmediate      = 0x87
	pinDataOut            = 1 << 1
	maxSWDTransferBits    = 16_384
)

// MaxTransferBits reports the conservative SWDIO limit used for packed
// transfers. It does not access the adapter.
func (c *Channel) MaxTransferBits() int { return maxSWDTransferBits }

type swdRead struct {
	offset int
	bits   int
}

// SWDIO executes one direction-explicit SWD bit stream.
func (c *Channel) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if bits < 0 || len(direction)*8 < bits || len(output)*8 < bits {
		return nil, fmt.Errorf("ftdi: invalid %d-bit SWD stream", bits)
	}
	commands, reads := swdCommands(direction, output, bits)
	if err := c.writeExact(ctx, commands); err != nil {
		return nil, err
	}
	var response []byte
	if len(reads) != 0 {
		var err error
		response, err = c.readPayload(ctx, len(reads))
		if err != nil {
			return nil, err
		}
	}
	return decodeSWD(response, reads, bits), nil
}

func swdCommands(direction, output []byte, bits int) ([]byte, []swdRead) {
	commands := make([]byte, 0, bits)
	var reads []swdRead
	for offset := 0; offset < bits; {
		hostDrives := streamBit(direction, offset)
		width := swdRunWidth(direction, offset, bits, hostDrives)
		if hostDrives {
			data := swdRunData(output, offset, width)
			commands = append(
				commands,
				cmdSetDataLow, (data&1)<<1, pinClock|pinDataOut,
				cmdClockBitsOutNegLSB, byte(width-1), data,
			)
		} else {
			commands = append(commands, cmdSetDataLow, 0, pinClock, cmdClockBitsInPosLSB, byte(width-1))
			reads = append(reads, swdRead{offset: offset, bits: width})
		}
		offset += width
	}
	commands = append(commands, cmdSetDataLow, 0, pinClock)
	if len(reads) != 0 {
		commands = append(commands, cmdSendImmediate)
	}
	return commands, reads
}

func swdRunWidth(direction []byte, offset, bits int, hostDrives bool) int {
	width := 1
	for width < 8 &&
		offset+width < bits &&
		streamBit(direction, offset+width) == hostDrives {
		width++
	}
	return width
}

func swdRunData(output []byte, offset, bits int) byte {
	var data byte
	for bit := range bits {
		if streamBit(output, offset+bit) {
			data |= 1 << uint(bit)
		}
	}
	return data
}

func decodeSWD(response []byte, reads []swdRead, bits int) []byte {
	input := make([]byte, (bits+7)/8)
	for index, read := range reads {
		samples := response[index] >> uint(8-read.bits)
		for bit := range read.bits {
			if samples>>uint(bit)&1 != 0 {
				input[(read.offset+bit)/8] |=
					1 << (uint(read.offset+bit) % 8)
			}
		}
	}
	return input
}

func streamBit(stream []byte, bit int) bool {
	return stream[bit/8]>>(uint(bit)%8)&1 != 0
}
