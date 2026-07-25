package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

// DebugPort accesses one SW-DP through an entered SWD connection.
type DebugPort struct {
	conn *swd.Conn
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
	value, err := dp.conn.Transfer(ctx, swd.Request{
		Read: true,
		Addr: uint8(reg),
	}, 0)
	if err != nil {
		return 0, fmt.Errorf("dap: read DP register %#02x: %w", reg, err)
	}
	return value, nil
}

// WriteDP writes reg in the currently selected debug-port bank.
func (dp *DebugPort) WriteDP(ctx context.Context, reg DPReg, value uint32) error {
	if dp == nil || dp.conn == nil {
		return errors.New("dap: nil SWD connection")
	}
	_, err := dp.conn.Transfer(ctx, swd.Request{Addr: uint8(reg)}, value)
	if err != nil {
		return fmt.Errorf("dap: write DP register %#02x: %w", reg, err)
	}
	return nil
}
