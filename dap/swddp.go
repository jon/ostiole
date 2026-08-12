package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

const dlcrTurnaroundMask = uint32(3 << 8)

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
	if err != nil {
		dp.state.loseFraming()
		return fmt.Errorf("dap: select DP bank zero before checking CTRL/STAT: %w", err)
	}
	dp.state.recordSELECT(0)
	if _, err := dp.transfer(ctx, swd.Request{Read: true, Addr: uint8(RDBUFF)}, 0); err != nil {
		return fmt.Errorf("dap: confirm DP bank zero before checking CTRL/STAT: %w", err)
	}
	return nil
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

// ReadDPAt reads one explicitly banked ADIv5 debug-port register. Nonzero
// banks require an active DPv1 or DPv2 connection; DPv3 uses the ADIv6 map.
func (dp *DebugPort) ReadDPAt(ctx context.Context, addr DPAddress) (uint32, error) {
	if err := dp.requireOperational(); err != nil {
		return 0, err
	}
	if err := dp.validateDPAddress(addr, false); err != nil {
		return 0, err
	}
	return dp.readDPAt(ctx, addr)
}

func (dp *DebugPort) readDPAt(ctx context.Context, addr DPAddress) (uint32, error) {
	if addr.Addr == CTRLSTAT {
		if err := dp.selectDPBank(ctx, addr.Bank); err != nil {
			return 0, err
		}
	}
	return dp.readDP(ctx, addr.Addr)
}

func (dp *DebugPort) readDP(ctx context.Context, reg DPReg) (uint32, error) {
	if dp == nil || dp.conn == nil {
		return 0, errors.New("dap: nil SWD connection")
	}
	if uint8(reg)&^0x0c != 0 {
		return 0, fmt.Errorf("dap: invalid DP register address %#02x", reg)
	}
	if reg == CTRLSTAT && dp.state.dpBankAmbiguous() {
		return 0, errors.New("dap: DP register bank is ambiguous after an unconfirmed SELECT write")
	}
	value, err := dp.transfer(ctx, swd.Request{
		Read: true,
		Addr: uint8(reg),
	}, 0)
	if err != nil {
		return 0, fmt.Errorf("dap: read DP register %#02x: %w", reg, err)
	}
	dp.recordDPRead(reg, value)
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

// WriteDPAt writes one explicitly banked ADIv5 debug-port register. Nonzero
// banks require an active DPv1 or DPv2 connection; DPv3 uses the ADIv6 map.
// Writes which require unsupported SWD framing are rejected before traffic.
// Release does not own power-request bits changed through this method. A
// successful DAPABORT write invalidates existing MemAP values.
func (dp *DebugPort) WriteDPAt(ctx context.Context, addr DPAddress, value uint32) error {
	if err := dp.requireOperational(); err != nil {
		return err
	}
	if err := dp.validateDPWrite(addr, value); err != nil {
		return err
	}
	return dp.writeDPAt(ctx, addr, value)
}

func (dp *DebugPort) writeDPAt(ctx context.Context, addr DPAddress, value uint32) error {
	if addr.Addr == CTRLSTAT {
		if err := dp.selectDPBank(ctx, addr.Bank); err != nil {
			return err
		}
	}
	return dp.writeDP(ctx, addr.Addr, value)
}

func (dp *DebugPort) selectDPBank(ctx context.Context, bank uint8) error {
	if dp.state.selectDP.valid && dp.state.dpBank() == bank {
		return dp.confirmPendingSELECT(ctx)
	}
	value := uint32(bank)
	if dp.state.selectDP.valid {
		value |= dp.state.selectDP.value &^ 0x0f
	}
	if err := dp.writeDP(ctx, SELECT, value); err != nil {
		return err
	}
	return dp.confirmPendingSELECT(ctx)
}

func (dp *DebugPort) confirmPendingSELECT(ctx context.Context) error {
	if !dp.state.selectPending {
		return nil
	}
	if _, err := dp.readDP(ctx, RDBUFF); err != nil {
		return fmt.Errorf("dap: confirm SELECT before banked DP access: %w", err)
	}
	return nil
}

func (dp *DebugPort) validateDPAddress(addr DPAddress, write bool) error {
	if uint8(addr.Addr)&^0x0c != 0 {
		return fmt.Errorf("dap: invalid DP register address %#02x", addr.Addr)
	}
	if addr.Bank > 0x0f {
		return fmt.Errorf("dap: DP register bank %d exceeds DPBANKSEL", addr.Bank)
	}
	if addr.Addr != CTRLSTAT {
		if addr.Bank != 0 {
			return fmt.Errorf("dap: DP register %#02x is not banked in ADIv5", addr.Addr)
		}
		if write && addr.Addr == RDBUFF {
			return errors.New("dap: RDBUFF is read-only in ADIv5")
		}
		return nil
	}
	if addr.Bank == 0 {
		return nil
	}
	return dp.validateBankedCTRLSTAT(addr.Bank, write)
}

func (dp *DebugPort) validateDPWrite(addr DPAddress, value uint32) error {
	if err := dp.validateDPAddress(addr, true); err != nil {
		return err
	}
	if addr.Addr == CTRLSTAT && addr.Bank == 0 && value&overrunDetect != 0 {
		return errors.New("dap: write CTRL/STAT: ORUNDETECT requires unsupported overrun-response framing")
	}
	if addr.Addr == CTRLSTAT && addr.Bank == 1 && value&dlcrTurnaroundMask != 0 {
		return errors.New("dap: write DLCR: variable turnaround requires unsupported SWD framing")
	}
	return nil
}

func (dp *DebugPort) validateBankedCTRLSTAT(bank uint8, write bool) error {
	if dp.state.session != sessionConnected || !dp.identified {
		return errors.New("dap: banked DP access requires an active connection")
	}
	if dp.identity.Version > 2 {
		return fmt.Errorf("dap: ADIv5 banked DP access does not support DPv%d", dp.identity.Version)
	}
	switch bank {
	case 1:
		if dp.identity.Version < 1 {
			return errors.New("dap: DLCR requires DPv1 or later")
		}
	case 2, 3, 4:
		if dp.identity.Version < 2 {
			return fmt.Errorf("dap: DP register bank %d requires DPv2 or later", bank)
		}
	default:
		return fmt.Errorf("dap: DP register bank %d is reserved in ADIv5", bank)
	}
	if write && bank >= 2 {
		return fmt.Errorf("dap: DP register bank %d is read-only in ADIv5", bank)
	}
	return nil
}

func (dp *DebugPort) writeDP(ctx context.Context, reg DPReg, value uint32) error {
	if dp == nil || dp.conn == nil {
		return errors.New("dap: nil SWD connection")
	}
	if err := dp.validateRawDPWrite(reg, value); err != nil {
		return err
	}
	_, err := dp.transfer(ctx, swd.Request{Addr: uint8(reg)}, value)
	if err != nil {
		return fmt.Errorf("dap: write DP register %#02x: %w", reg, err)
	}
	dp.recordDPWrite(reg, value)
	return nil
}

func (dp *DebugPort) validateRawDPWrite(reg DPReg, value uint32) error {
	if uint8(reg)&^0x0c != 0 {
		return fmt.Errorf("dap: invalid DP register address %#02x", reg)
	}
	if reg == CTRLSTAT && dp.state.dpBankAmbiguous() {
		return errors.New("dap: DP register bank is ambiguous after an unconfirmed SELECT write")
	}
	if reg == CTRLSTAT && dp.state.selectDP.valid && dp.state.dpBank() == 0 && value&overrunDetect != 0 {
		return errors.New("dap: write CTRL/STAT: ORUNDETECT requires unsupported overrun-response framing")
	}
	if reg == CTRLSTAT && dp.state.selectDP.valid && dp.state.dpBank() == 1 && value&dlcrTurnaroundMask != 0 {
		return errors.New("dap: write DLCR: variable turnaround requires unsupported SWD framing")
	}
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

func (dp *DebugPort) recordDPRead(reg DPReg, value uint32) {
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
