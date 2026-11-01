package jtag

import (
	"context"
	"errors"
)

// MaxScanBits bounds an individual scan or idle request to 1,048,576 clocks.
// Wire calls can be much smaller; the connection preserves state across them.
const MaxScanBits = 1 << 20

// ScanIR captures, shifts, and updates the whole instruction chain, ending in
// Idle. The final data bit is clocked while exiting ShiftIR. Reset must have
// established state. Do not use raw scans while a higher owner uses this Conn.
// A started scan finishes under WithCompletionTimeout before reporting caller
// cancellation. A non-nil capture means the scan and exit to Idle completed.
func (c *Conn) ScanIR(ctx context.Context, data []byte, bits int) ([]byte, error) {
	return c.scan(ctx, data, bits, ShiftIR)
}

// ScanDR captures, shifts, and updates the whole data chain, ending in Idle.
// Data-register updates can write target state. Buffer padding is ignored.
// Completion and cancellation follow ScanIR's contract.
func (c *Conn) ScanDR(ctx context.Context, data []byte, bits int) ([]byte, error) {
	return c.scan(ctx, data, bits, ShiftDR)
}

func (c *Conn) scan(ctx context.Context, data []byte, bits int, shift State) (capture []byte, err error) {
	if _, err := c.validate(ctx); err != nil {
		return nil, err
	}
	if bits <= 0 || bits > MaxScanBits || len(data) < (bits+7)/8 {
		return nil, errors.New("jtag: invalid scan length or buffer")
	}
	serial := c.serial
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.completionTimeout)
	defer cancel()
	defer func() { err = c.finishScan(ctx, serial, err) }()
	// Always enter through Idle so a scan captures a new register, including
	// when a previous explicit Move left the TAP in a shift or pause state.
	if err := c.Move(completionCtx, Idle); err != nil {
		return nil, err
	}
	if err := c.Move(completionCtx, shift); err != nil {
		return nil, err
	}
	tms := make([]byte, (bits+7)/8)
	put(tms, bits-1, 1)
	output, err := c.clock(completionCtx, tms, data, bits)
	if err != nil {
		return nil, err
	}
	if err := c.Move(completionCtx, Idle); err != nil {
		return nil, err
	}
	return output, nil
}

func (c *Conn) finishScan(ctx context.Context, serial uint64, err error) error {
	if err != nil && c.serial != serial {
		c.state = Unknown
	}
	if cause := ctx.Err(); cause != nil && !errors.Is(err, cause) {
		return errors.Join(err, cause)
	}
	return err
}

// Idle enters Run-Test/Idle and clocks cycles with TMS and TDI low. Zero is
// a no-op, including when not already in Idle. Idle clocks can run target work.
func (c *Conn) Idle(ctx context.Context, cycles int) error {
	if _, err := c.validate(ctx); err != nil {
		return err
	}
	if cycles < 0 || cycles > MaxScanBits {
		return errors.New("jtag: invalid idle count")
	}
	if cycles == 0 {
		return nil
	}
	if err := c.Move(ctx, Idle); err != nil {
		return err
	}
	zeros := make([]byte, (cycles+7)/8)
	_, err := c.clock(ctx, zeros, zeros, cycles)
	return err
}
