package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

// DebugPort accesses one SW-DP through an entered SWD connection.
type DebugPort struct {
	conn            *swd.Conn
	identity        DPIDRInfo
	identified      bool
	connected       bool
	ownedPower      uint32
	minimal         bool
	apEpoch         uint64
	framingLost     bool
	overrunDisabled bool
	dpBank          uint8
	dpBankKnown     bool
}

func (dp *DebugPort) selectDPBankZero(ctx context.Context) error {
	_, err := dp.conn.Transfer(ctx, swd.Request{Addr: uint8(SELECT)}, 0)
	if err == nil {
		dp.dpBank = 0
		dp.dpBankKnown = true
		return nil
	}
	dp.apEpoch++
	dp.framingLost = true
	dp.dpBankKnown = false
	return fmt.Errorf("dap: select DP bank zero before checking CTRL/STAT: %w", err)
}

// NewSWDP returns a debug-port client over conn.
//
// The caller remains responsible for entering SWD protocol mode first.
func NewSWDP(conn *swd.Conn) *DebugPort {
	return &DebugPort{conn: conn}
}

// ReadDP reads reg in the currently selected debug-port bank.
func (dp *DebugPort) ReadDP(ctx context.Context, reg DPReg) (uint32, error) {
	if dp == nil || dp.conn == nil {
		return 0, errors.New("dap: nil SWD connection")
	}
	value, err := dp.transfer(ctx, swd.Request{
		Read: true,
		Addr: uint8(reg),
	}, 0)
	if err != nil {
		return 0, fmt.Errorf("dap: read DP register %#02x: %w", reg, err)
	}
	if reg == CTRLSTAT && dp.dpBankKnown && dp.dpBank == 0 {
		dp.overrunDisabled = value&overrunDetect == 0
	}
	return value, nil
}

// WriteDP writes reg in the currently selected debug-port bank.
func (dp *DebugPort) WriteDP(ctx context.Context, reg DPReg, value uint32) error {
	if dp == nil || dp.conn == nil {
		return errors.New("dap: nil SWD connection")
	}
	if reg == CTRLSTAT && dp.dpBankKnown && dp.dpBank == 0 && value&overrunDetect != 0 {
		return errors.New("dap: write CTRL/STAT: ORUNDETECT requires unsupported overrun-response framing")
	}
	_, err := dp.transfer(ctx, swd.Request{Addr: uint8(reg)}, value)
	if err != nil {
		return fmt.Errorf("dap: write DP register %#02x: %w", reg, err)
	}
	if reg == SELECT {
		dp.dpBank = uint8(value & 0x0f)
		dp.dpBankKnown = true
	}
	if reg == CTRLSTAT && dp.dpBankKnown && dp.dpBank == 0 {
		dp.overrunDisabled = value&overrunDetect == 0
	}
	return nil
}
