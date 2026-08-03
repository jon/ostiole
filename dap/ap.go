package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

// APSel identifies one access port.
type APSel uint8

// APReg identifies one banked access-port register.
type APReg uint8

// APIDR is the access-port identification register.
const APIDR APReg = 0xfc

// ReadAP reads one selected access-port register through the posted pipeline.
func (dp *DebugPort) ReadAP(ctx context.Context, sel APSel, reg APReg) (uint32, error) {
	if err := dp.requireConnected(); err != nil {
		return 0, err
	}
	if err := validateAPReg(reg); err != nil {
		return 0, err
	}
	if err := dp.selectAP(ctx, sel, reg); err != nil {
		return 0, err
	}
	_, err := dp.conn.Transfer(ctx, swd.Request{
		AP:   true,
		Read: true,
		Addr: uint8(reg) & 0x0c,
	}, 0)
	if err != nil {
		return 0, fmt.Errorf("dap: post AP register %#02x read: %w", reg, err)
	}
	return dp.ReadDP(ctx, RDBUFF)
}

// WriteAP writes one selected access-port register and waits for completion.
func (dp *DebugPort) WriteAP(ctx context.Context, sel APSel, reg APReg, value uint32) error {
	if err := dp.requireConnected(); err != nil {
		return err
	}
	if err := validateAPReg(reg); err != nil {
		return err
	}
	if err := dp.selectAP(ctx, sel, reg); err != nil {
		return err
	}
	_, err := dp.conn.Transfer(ctx, swd.Request{
		AP:   true,
		Addr: uint8(reg) & 0x0c,
	}, value)
	if err != nil {
		return fmt.Errorf("dap: write AP register %#02x: %w", reg, err)
	}
	if _, err := dp.ReadDP(ctx, RDBUFF); err != nil {
		return fmt.Errorf("dap: complete AP register %#02x write: %w", reg, err)
	}
	return nil
}

func (dp *DebugPort) selectAP(ctx context.Context, sel APSel, reg APReg) error {
	value := uint32(sel)<<24 | uint32(reg&0xf0)
	return dp.WriteDP(ctx, SELECT, value)
}

func validateAPReg(reg APReg) error {
	if reg&3 != 0 {
		return fmt.Errorf("dap: unaligned AP register %#02x", reg)
	}
	return nil
}

func (dp *DebugPort) requireConnected() error {
	if dp == nil || !dp.connected {
		return errors.New("dap: SW-DP is not connected")
	}
	return nil
}
