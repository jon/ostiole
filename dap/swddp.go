package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

// DebugPort accesses one SW-DP through an entered SWD connection.
//
// Calls to a DebugPort and its underlying connection must be serialized.
// DebugPort caches protocol state, so do not use the connection directly while
// the debug port remains in use.
type DebugPort struct {
	conn         *swd.Conn
	identity     DPIDRInfo
	identified   bool
	reentryID    DPIDRInfo
	reentryKnown bool
	state        debugPortState
}

func (dp *DebugPort) selectDPBankZero(ctx context.Context) error {
	_, err := dp.conn.Transfer(ctx, swd.Request{Addr: uint8(SELECT)}, 0)
	if err == nil {
		dp.state.recordSELECT(0)
		return nil
	}
	dp.state.loseFraming()
	return fmt.Errorf("dap: select DP bank zero before checking CTRL/STAT: %w", err)
}

// NewSWDP returns a debug-port client over conn. The caller must give the
// returned client exclusive use of conn's SWD transaction stream until the
// client is no longer used.
//
// The caller remains responsible for entering SWD protocol mode first. Do not
// call conn.Transfer directly while using the returned DebugPort; doing so can
// invalidate its cached register selection and response state.
func NewSWDP(conn *swd.Conn) *DebugPort {
	return &DebugPort{conn: conn}
}

// ReadDP reads reg in the currently selected debug-port bank.
//
// Raw DP access does not require Connect, but it fails while cleanup is
// pending.
func (dp *DebugPort) ReadDP(ctx context.Context, reg DPReg) (uint32, error) {
	if err := dp.requireOperational(); err != nil {
		return 0, err
	}
	return dp.readDP(ctx, reg)
}

func (dp *DebugPort) readDP(ctx context.Context, reg DPReg) (uint32, error) {
	if dp == nil || dp.conn == nil {
		return 0, errors.New("dap: nil SWD connection")
	}
	if uint8(reg)&^0x0c != 0 {
		return 0, fmt.Errorf("dap: invalid DP register address %#02x", reg)
	}
	value, err := dp.transfer(ctx, swd.Request{
		Read: true,
		Addr: uint8(reg),
	}, 0)
	if err != nil {
		return 0, fmt.Errorf("dap: read DP register %#02x: %w", reg, err)
	}
	if reg == CTRLSTAT && dp.state.selectDP.valid && dp.state.dpBank() == 0 {
		dp.state.confirmResponse(value)
	}
	return value, nil
}

// WriteDP writes reg in the currently selected debug-port bank.
//
// Raw DP access does not require Connect, but it fails while cleanup is
// pending. Release does not own or restore power-request bits changed through
// raw access. A successful DAPABORT write invalidates existing MemAP values.
func (dp *DebugPort) WriteDP(ctx context.Context, reg DPReg, value uint32) error {
	if err := dp.requireOperational(); err != nil {
		return err
	}
	return dp.writeDP(ctx, reg, value)
}

func (dp *DebugPort) writeDP(ctx context.Context, reg DPReg, value uint32) error {
	if dp == nil || dp.conn == nil {
		return errors.New("dap: nil SWD connection")
	}
	if uint8(reg)&^0x0c != 0 {
		return fmt.Errorf("dap: invalid DP register address %#02x", reg)
	}
	if reg == CTRLSTAT && dp.state.selectDP.valid && dp.state.dpBank() == 0 && value&overrunDetect != 0 {
		return errors.New("dap: write CTRL/STAT: ORUNDETECT requires unsupported overrun-response framing")
	}
	_, err := dp.transfer(ctx, swd.Request{Addr: uint8(reg)}, value)
	if err != nil {
		return fmt.Errorf("dap: write DP register %#02x: %w", reg, err)
	}
	dp.recordDPWrite(reg, value)
	return nil
}

func (dp *DebugPort) recordDPWrite(reg DPReg, value uint32) {
	if reg == SELECT {
		dp.state.recordSELECT(value)
	}
	if reg == ABORT && value&dapAbort != 0 {
		dp.state.invalidateAP()
	}
	if reg == CTRLSTAT && dp.state.selectDP.valid && dp.state.dpBank() == 0 {
		dp.state.confirmResponse(value)
	}
}

func (dp *DebugPort) requireOperational() error {
	if dp == nil || dp.conn == nil {
		return errors.New("dap: nil SWD connection")
	}
	if dp.state.session == sessionRepairRequired {
		return dp.repairPendingError()
	}
	return nil
}

func (dp *DebugPort) repairPendingError() error {
	err := errors.New("dap: debug-port cleanup is pending")
	if dp.state.response == responseLost {
		return errors.Join(err, errFramingUnknown)
	}
	return err
}
