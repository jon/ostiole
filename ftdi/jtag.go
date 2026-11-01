package ftdi

import (
	"context"
)

const (
	pinTMS = 1 << 3
	// One TMS command can return a byte per clock. Bound that worst-case
	// response by the receive window already posted for SWD input.
	maxJTAGTransferBits = maxSWDTransferBits / 2
)

// JTAGIO drives packed LSB-first TMS/TDI and samples TDO on rising TCK edges.
// TMS and TDI change on falling edges. Nonempty calls wait for an MPSSE echo
// after the final command. An incomplete accepted exchange poisons the channel.
// Zero bits sends no traffic.
func (c *Channel) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	return c.clockStream(ctx, tms, tdi, bits, "JTAG", jtagCommands)
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
