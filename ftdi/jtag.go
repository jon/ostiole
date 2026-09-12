package ftdi

import (
	"context"
	"fmt"
)

const (
	pinTMS = 1 << 3
	// One TMS command can return a byte per clock. Bound that worst-case
	// response by the receive window already posted for SWD input.
	maxJTAGTransferBits = maxSWDTransferBits / 2
)

// JTAGIO drives packed LSB-first TMS/TDI and samples TDO on rising TCK edges.
// TMS and TDI change on falling edges. Zero bits sends no traffic.
func (c *Channel) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	if err := c.wireReady(ctx); err != nil {
		return nil, err
	}
	if bits < 0 || bits > maxJTAGTransferBits || len(tms) < (bits+7)/8 || len(tdi) < (bits+7)/8 {
		return nil, fmt.Errorf("ftdi: invalid %d-bit JTAG stream", bits)
	}
	if bits == 0 {
		return []byte{}, nil
	}
	commands, reads := jtagCommands(tms, tdi, bits)
	response, err := c.exchangePayload(ctx, commands, len(reads))
	if err != nil {
		return nil, err
	}
	return decodeSWD(response, reads, bits), nil
}

func jtagCommands(tms, tdi []byte, bits int) ([]byte, []swdRead) {
	commands := []byte{cmdSetDataLow, 0, pinClock | pinDataOut | pinTMS}
	var reads []swdRead
	for offset := 0; offset < bits; {
		width := 0
		for width < 8 && offset+width < bits && !streamBit(tms, offset+width) {
			width++
		}
		if width > 0 {
			commands = append(commands, cmdSetDataLow, 0, pinClock|pinDataOut|pinTMS,
				0x3b, byte(width-1), swdRunData(tdi, offset, width))
		} else {
			width = 1
			for width < 7 && offset+width < bits && streamBit(tdi, offset+width) == streamBit(tdi, offset) {
				width++
			}
			data := swdRunData(tms, offset, width)
			if streamBit(tdi, offset) {
				data |= 0x80
			}
			commands = append(commands, 0x6b, byte(width-1), data)
		}
		reads = append(reads, swdRead{offset: offset, bits: width})
		offset += width
	}
	return append(commands, cmdSendImmediate), reads
}
