package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

const dlcrTurnaroundMask = uint32(3 << 8)

// DebugPort enters and accesses one SW-DP through an SWD connection.
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
	err := dp.conn.WriteDP(ctx, dpRegisterOffset(SELECT), 0)
	if err != nil {
		dp.state.loseFraming()
		return fmt.Errorf("dap: select DP bank zero before checking CTRL/STAT: %w", err)
	}
	dp.recordDPWrite(SELECT, 0)
	if _, err := dp.readDP(ctx, RDBUFF); err != nil {
		return fmt.Errorf("dap: confirm DP bank zero before checking CTRL/STAT: %w", err)
	}
	return nil
}

// NewDebugPort returns a debug-port client over conn. Connect performs SWD
// protocol entry. The caller must give the returned client exclusive use of
// conn's transaction stream until the client is no longer used. Do not call
// conn.ReadDP, conn.WriteDP, conn.ReadAP, conn.WriteAP, or conn.JTAGToSWD while
// using the DebugPort; doing so can invalidate its cached register selection
// and response state.
func NewDebugPort(conn *swd.Conn) *DebugPort {
	return &DebugPort{conn: conn}
}

// ReadDP reads one logical ADIv5 debug-port register. Bank-independent and
// bank-zero registers remain distinct. Nonzero banks require an active DPv1 or
// DPv2 connection; DPv3 uses the ADIv6 map. The debug port must be connected.
func (dp *DebugPort) ReadDP(ctx context.Context, reg DPRegister) (uint32, error) {
	if err := dp.requireOperational(); err != nil {
		return 0, err
	}
	return dp.readDP(ctx, reg)
}

func (dp *DebugPort) readDP(ctx context.Context, reg DPRegister) (uint32, error) {
	if dp == nil || dp.conn == nil {
		return 0, errors.New("dap: nil SWD connection")
	}
	info, err := dp.validateDPRegister(reg, false)
	if err != nil {
		return 0, err
	}
	if !info.bankIndependent && dp.state.dpBankAmbiguous() {
		return 0, errors.New("dap: DP register bank is ambiguous after an unconfirmed SELECT write")
	}
	if !info.bankIndependent {
		if err := dp.selectDPBank(ctx, info.bank); err != nil {
			return 0, err
		}
	}
	return dp.readDPRegister(ctx, reg, info)
}

func (dp *DebugPort) readDPRegister(ctx context.Context, reg DPRegister, info dpRegisterInfo) (uint32, error) {
	var value uint32
	var err error
	if reg == RDBUFF && dp.state.dpWritePending {
		value, err = dp.transferDPWriteBarrier(ctx)
	} else {
		value, err = dp.transfer(ctx, dpTransferRequest(reg, true), 0)
	}
	if err != nil {
		return 0, fmt.Errorf("dap: read %s: %w", info.name, err)
	}
	dp.recordDPRead(reg, value)
	return value, nil
}

// WriteDP writes one logical ADIv5 debug-port register. Writes that would
// enable an unsupported SWD response mode or turnaround are rejected before
// traffic. Release does not own power-request bits changed through this method.
// A successful DAPABORT write invalidates existing MemAP values. The debug port
// must be connected.
func (dp *DebugPort) WriteDP(ctx context.Context, reg DPRegister, value uint32) error {
	if err := dp.requireOperational(); err != nil {
		return err
	}
	return dp.writeDP(ctx, reg, value)
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

func (dp *DebugPort) validateDPRegister(reg DPRegister, write bool) (dpRegisterInfo, error) {
	info, ok := describeDPRegister(reg)
	if !ok {
		return dpRegisterInfo{}, fmt.Errorf("dap: invalid DP register %#04x", uint16(reg))
	}
	if write && !info.writable {
		return dpRegisterInfo{}, fmt.Errorf("dap: %s is read-only", info.name)
	}
	if !write && !info.readable {
		return dpRegisterInfo{}, fmt.Errorf("dap: %s is write-only", info.name)
	}
	if !info.bankIndependent && info.bank != 0 {
		if err := dp.validateBankedDPRegister(info); err != nil {
			return dpRegisterInfo{}, err
		}
	}
	return info, nil
}

func (dp *DebugPort) validateDPWrite(reg DPRegister, value uint32) (dpRegisterInfo, error) {
	info, err := dp.validateDPRegister(reg, true)
	if err != nil {
		return dpRegisterInfo{}, err
	}
	if reg == CTRLSTAT && value&overrunDetect != 0 {
		return dpRegisterInfo{}, errors.New("dap: write CTRL/STAT: DebugPort cannot enable ORUNDETECT while using simple responses")
	}
	if reg == DLCR && value&dlcrTurnaroundMask != 0 {
		return dpRegisterInfo{}, errors.New("dap: write DLCR: variable turnaround requires unsupported SWD framing")
	}
	return info, nil
}

func (dp *DebugPort) validateBankedDPRegister(info dpRegisterInfo) error {
	if dp.state.session != sessionConnected || !dp.identified {
		return errors.New("dap: banked DP access requires an active connection")
	}
	if dp.identity.Version > 2 {
		return fmt.Errorf("dap: ADIv5 banked DP access does not support DPv%d", dp.identity.Version)
	}
	if dp.identity.Version < info.minVersion {
		return fmt.Errorf("dap: %s requires DPv%d or later", info.name, info.minVersion)
	}
	return nil
}

func (dp *DebugPort) writeDP(ctx context.Context, reg DPRegister, value uint32) error {
	if dp == nil || dp.conn == nil {
		return errors.New("dap: nil SWD connection")
	}
	info, err := dp.validateDPWrite(reg, value)
	if err != nil {
		return err
	}
	if !info.bankIndependent && dp.state.dpBankAmbiguous() {
		return errors.New("dap: DP register bank is ambiguous after an unconfirmed SELECT write")
	}
	if !info.bankIndependent {
		if err := dp.selectDPBank(ctx, info.bank); err != nil {
			return err
		}
	}
	_, err = dp.transfer(ctx, dpTransferRequest(reg, false), value)
	if err != nil {
		return fmt.Errorf("dap: write %s: %w", info.name, err)
	}
	dp.recordDPWrite(reg, value)
	return nil
}

func (dp *DebugPort) recordDPWrite(reg DPRegister, value uint32) {
	dp.recordDPWriteState(reg, value)
	if reg == ABORT && value&dapAbort != 0 {
		dp.state.invalidateAP()
	}
}

func (dp *DebugPort) recordDPWriteState(reg DPRegister, value uint32) {
	dp.state.beginDPWrite()
	if reg == SELECT {
		dp.state.recordSELECT(value)
	}
	if reg == CTRLSTAT && dp.state.selectDP.valid && dp.state.dpBank() == 0 {
		dp.state.confirmResponse(value)
	}
}

func (dp *DebugPort) recordDPRead(reg DPRegister, value uint32) {
	if reg != DPIDR {
		dp.state.settleDPWrite()
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
	if dp.state.session != sessionConnected {
		return errors.New("dap: SW-DP is not connected")
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
