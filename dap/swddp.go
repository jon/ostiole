package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

const dlcrTurnaroundMask = uint32(3 << 8)

// Option configures a DebugPort during construction. NewDebugPort applies
// options in order and ignores zero Option values.
type Option struct {
	apply func(*debugPortOptions)
}

type debugPortOptions struct {
	maxWaits uint
}

// WithMaxWaits limits clean WAIT responses for one physical request without
// sending traffic. Reaching a nonzero limit returns swd.ErrWait. One disables
// WAIT retries; zero leaves retrying bounded only by the operation context. The
// limit counts responses, not time, and does not bound a blocked host call.
func WithMaxWaits(maxWaits uint) Option {
	return Option{apply: func(options *debugPortOptions) {
		options.maxWaits = maxWaits
	}}
}

// DebugPort enters and accesses one SW-DP through an SWD connection.
//
// Calls to a DebugPort and its underlying connection must be serialized.
// DebugPort caches protocol state, so do not use the connection directly while
// the debug port remains in use. A DebugPort retries the same physical request
// after a clean WAIT until its configured limit is reached or the operation
// context ends. It does not retry a FAULT. If the context ends, errors.Is
// reports the context error and the original WAIT is not retained as
// swd.ErrWait. Independently joined cleanup failures remain visible.
type DebugPort struct {
	conn         *swd.Conn
	maxWaits     uint
	identity     DPIDRInfo
	identified   bool
	reentryID    DPIDRInfo
	reentryKnown bool
	state        debugPortState
}

// NewDebugPort returns a debug-port client over conn. The one-argument form
// retries a clean WAIT while the operation context remains active. Built-in
// options send no traffic. Connect performs SWD protocol entry. The caller must
// give the returned client exclusive use of conn's transaction stream until
// the client is no longer used. Do not call conn.Connect, conn.Release,
// conn.ReadDP, conn.WriteDP, conn.ReadAP, conn.WriteAP, or conn.JTAGToSWD while
// using the DebugPort; doing so can invalidate its cached register selection
// and response state.
func NewDebugPort(conn *swd.Conn, options ...Option) *DebugPort {
	config := debugPortOptions{}
	for _, option := range options {
		if option.apply != nil {
			option.apply(&config)
		}
	}
	return &DebugPort{conn: conn, maxWaits: config.maxWaits}
}

// SetMaxWaits changes the clean WAIT response limit while the debug port is
// idle. It sends no traffic. Zero uses only the operation context; one disables
// WAIT retries. SetMaxWaits returns an error while the port is connected or
// cleanup is pending. It can be called again after a successful Release.
func (dp *DebugPort) SetMaxWaits(maxWaits uint) error {
	if dp == nil {
		return errors.New("dap: cannot set maximum WAIT responses on nil debug port")
	}
	if dp.state.session != sessionIdle {
		return errors.New("dap: cannot set maximum WAIT responses unless debug port is idle")
	}
	dp.maxWaits = maxWaits
	return nil
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

// WriteDP writes one logical ADIv5 debug-port register. The SWD connection owns
// CTRL/STAT.ORUNDETECT, so writes must preserve that bit. Writes that require
// unsupported turnaround framing are rejected before traffic. Release does
// not restore power-request bits changed through this method. A successful
// DAPABORT write invalidates existing MemAP values. The debug port must be
// connected.
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
	if reg == CTRLSTAT && !dp.state.responseKnown() {
		return dpRegisterInfo{}, errors.New("dap: write CTRL/STAT requires a known SWD response grammar")
	}
	if reg == CTRLSTAT && (value&overrunDetect != 0) != (dp.state.response == responseOverrun) {
		return dpRegisterInfo{}, errors.New("dap: write CTRL/STAT: ORUNDETECT is owned by the SWD connection")
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
	if err := dp.prepareDPWrite(ctx, reg, info); err != nil {
		return err
	}
	_, err = dp.transfer(ctx, dpTransferRequest(reg, false), value)
	if err != nil {
		return fmt.Errorf("dap: write %s: %w", info.name, err)
	}
	dp.recordDPWrite(reg, value)
	return nil
}

func (dp *DebugPort) prepareDPWrite(ctx context.Context, reg DPRegister, info dpRegisterInfo) error {
	if !info.bankIndependent && dp.state.dpBankAmbiguous() {
		return errors.New("dap: DP register bank is ambiguous after an unconfirmed SELECT write")
	}
	if !info.bankIndependent {
		if err := dp.selectDPBank(ctx, info.bank); err != nil {
			return err
		}
	}
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
}

func (dp *DebugPort) recordDPRead(reg DPRegister, value uint32) {
	if reg != DPIDR {
		dp.state.settleDPWrite()
	}
	if reg == CTRLSTAT && dp.state.selectDP.valid && dp.state.dpBank() == 0 {
		dp.confirmResponse(value)
	}
}

func (dp *DebugPort) confirmResponse(state uint32) {
	dp.state.confirmResponse(state)
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
