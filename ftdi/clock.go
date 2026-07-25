package ftdi

import (
	"context"
	"fmt"
)

const (
	baseClockHz = 60_000_000
	minClockHz  = (baseClockHz + 2*(1<<16) - 1) / (2 * (1 << 16))

	cmdSetDataLow      = 0x80
	cmdSetDataHigh     = 0x82
	cmdDisableLoop     = 0x85
	cmdSetClockDiv     = 0x86
	cmdDisableDivBy5   = 0x8a
	cmdDisable3Phase   = 0x8d
	cmdDisableAdaptive = 0x97
	pinClock           = 1 << 0
)

// Configure selects a conservative clock and the idle SWD pin state.
func (c *Channel) Configure(ctx context.Context) error {
	divisor, err := clockDivisor(c.clockHz)
	if err != nil {
		return err
	}
	return c.writeExact(ctx, []byte{
		cmdDisableDivBy5,
		cmdDisableAdaptive,
		cmdDisable3Phase,
		cmdSetClockDiv, byte(divisor), byte(divisor >> 8),
		cmdDisableLoop,
		cmdSetDataLow, 0, pinClock,
		cmdSetDataHigh, 0, 0,
	})
}

func clockDivisor(clockHz uint32) (uint16, error) {
	if clockHz < minClockHz {
		return 0, fmt.Errorf(
			"ftdi: clock %d Hz is below the attainable minimum",
			clockHz,
		)
	}
	denominator := 2 * uint64(clockHz)
	ratio := (uint64(baseClockHz) + denominator - 1) / denominator
	if ratio <= 1 {
		return 0, nil
	}
	if ratio > 1<<16 {
		return 0, fmt.Errorf(
			"ftdi: clock %d Hz is below the attainable minimum",
			clockHz,
		)
	}
	return uint16(ratio - 1), nil
}
