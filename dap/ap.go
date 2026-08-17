package dap

import (
	"context"
	"errors"
	"fmt"
)

// APSel identifies one access port. Its zero value is invalid; construct a
// selector with NewAPSel.
type APSel struct {
	index uint16
}

// NewAPSel returns the selector for one ADIv5 access port.
func NewAPSel(value uint8) APSel {
	return APSel{index: uint16(value) + 1}
}

// Value returns the architectural selector value. The zero APSel returns an
// error.
func (sel APSel) Value() (uint8, error) {
	if sel.index == 0 {
		return 0, errors.New("dap: zero APSel is invalid")
	}
	return uint8(sel.index - 1), nil
}

// APReg identifies one banked access-port register.
type APReg uint8

// APIDR is the access-port identification register.
const APIDR APReg = 0xfc

// ReadAP reads one selected access-port register through the posted pipeline.
// The debug port must be connected and have no pending cleanup.
func (dp *DebugPort) ReadAP(ctx context.Context, sel APSel, reg APReg) (uint32, error) {
	if err := dp.requireConnected(); err != nil {
		return 0, err
	}
	return dp.readAP(ctx, sel, reg)
}

func (dp *DebugPort) readAP(ctx context.Context, sel APSel, reg APReg) (uint32, error) {
	if err := validateAPReg(reg); err != nil {
		return 0, err
	}
	if err := dp.selectAP(ctx, sel, reg); err != nil {
		return 0, err
	}
	_, err := dp.transfer(ctx, transferRequest{
		AP:   true,
		Read: true,
		Addr: uint8(reg) & 0x0c,
	}, 0)
	if err != nil {
		return 0, fmt.Errorf("dap: post AP register %#02x read: %w", reg, err)
	}
	return dp.readDP(ctx, RDBUFF)
}

// WriteAP writes one selected access-port register and waits for completion.
// The debug port must be connected and have no pending cleanup.
func (dp *DebugPort) WriteAP(ctx context.Context, sel APSel, reg APReg, value uint32) error {
	if err := dp.requireConnected(); err != nil {
		return err
	}
	return dp.writeAP(ctx, sel, reg, value)
}

func (dp *DebugPort) writeAP(ctx context.Context, sel APSel, reg APReg, value uint32) error {
	if err := validateAPReg(reg); err != nil {
		return err
	}
	if err := dp.selectAP(ctx, sel, reg); err != nil {
		return err
	}
	_, err := dp.transfer(ctx, transferRequest{
		AP:   true,
		Addr: uint8(reg) & 0x0c,
	}, value)
	if err != nil {
		return fmt.Errorf("dap: write AP register %#02x: %w", reg, err)
	}
	if _, err := dp.readDP(ctx, RDBUFF); err != nil {
		return fmt.Errorf("dap: complete AP register %#02x write: %w", reg, err)
	}
	return nil
}

func (dp *DebugPort) selectAP(ctx context.Context, sel APSel, reg APReg) error {
	selection, err := sel.Value()
	if err != nil {
		return err
	}
	value := uint32(selection)<<24 | uint32(reg&0xf0)
	if dp.state.selectDP.valid && dp.state.selectDP.value == value {
		return nil
	}
	return dp.writeDP(ctx, SELECT, value)
}

func validateAPReg(reg APReg) error {
	if reg&3 != 0 {
		return fmt.Errorf("dap: unaligned AP register %#02x", reg)
	}
	return nil
}

func (dp *DebugPort) requireConnected() error {
	if dp == nil || dp.conn == nil || dp.state.session == sessionIdle {
		return errors.New("dap: SW-DP is not connected")
	}
	if dp.state.session == sessionRepairRequired {
		return dp.repairPendingError()
	}
	return nil
}
