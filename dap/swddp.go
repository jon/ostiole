package dap

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const dlcrTurnaroundMask = uint32(3 << 8)

// Option configures a DebugPort during construction. NewDebugPort applies
// options in order and ignores zero Option values.
type Option struct {
	apply func(*debugPortOptions)
}

type debugPortOptions struct {
	maxWaits       uint
	cleanupTimeout time.Duration
}

// WithCleanupTimeout sets the independent budget for each recovery attempt.
// The default is one second for SWD and thirty seconds for JTAG. Connect
// rejects nonpositive durations before traffic. Ordinary operations remain
// bounded by their caller's context.
func WithCleanupTimeout(timeout time.Duration) Option {
	return Option{apply: func(options *debugPortOptions) {
		options.cleanupTimeout = timeout
	}}
}

// WithMaxWaits limits WAIT observations for one request or pending completion
// without traffic. Reaching a nonzero limit reports ErrWait. One stops at the
// first WAIT; zero uses only the operation context. The limit counts responses,
// not time, and does not bound a blocked host call.
func WithMaxWaits(maxWaits uint) Option {
	return Option{apply: func(options *debugPortOptions) {
		options.maxWaits = maxWaits
	}}
}

// DebugPort enters and accesses one SW-DP or baseline ADIv5 JTAG-DP.
//
// Calls to a DebugPort and its underlying connection or chain must be
// serialized. Give the debug port exclusive use until Release succeeds.
// SWD retries rejected requests; JTAG polls an accepted operation without
// replaying it. Neither replays faults or ambiguous transfers. Cancellation
// returns the context error together with any cleanup failure.
// Nil contexts are rejected before traffic or changes to protocol state.
type DebugPort struct {
	conn           *swdExecutor
	jtag           *jtagExecutor
	maxWaits       uint
	cleanupTimeout time.Duration
	identity       Identity
	identified     bool
	addressBits    uint8
	reentryID      Identity
	reentryKnown   bool
	state          debugPortState
}

// NewDebugPort returns a client for port without traffic. Use SWDP or JTAGDP
// to construct the binding. Connect validates it and performs protocol entry.
// The client never closes the probe. Release acquired MemAPs, then the debug
// port, before closing the probe; retain dependencies after a cleanup failure.
func NewDebugPort(port Port, options ...Option) *DebugPort {
	config := debugPortOptions{cleanupTimeout: time.Second}
	if port.jtag {
		config.cleanupTimeout = 30 * time.Second
	}
	for _, option := range options {
		if option.apply != nil {
			option.apply(&config)
		}
	}
	dp := &DebugPort{maxWaits: config.maxWaits, cleanupTimeout: config.cleanupTimeout}
	dp.conn = newSWDExecutor(port.swd, dp)
	if port.jtag {
		dp.jtag = &jtagExecutor{dp: dp, chain: port.chain, index: port.tapIndex}
	}
	return dp
}

func (dp *DebugPort) cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), dp.cleanupTimeout)
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

// ReadDP reads one logical debug-port register. Bank-independent and
// bank-zero registers remain distinct. Nonzero banks require an active SW-DP
// DPv1, DPv2, or DPv3 connection. Baseline JTAG-DP has no banked DP registers or DPIDR;
// it supplies IDCODE instead. The debug port must be connected.
func (dp *DebugPort) ReadDP(ctx context.Context, reg DPRegister) (uint32, error) {
	if err := dp.requireOperational(ctx); err != nil {
		return 0, err
	}
	return dp.readDP(ctx, reg)
}

func (dp *DebugPort) readDP(ctx context.Context, reg DPRegister) (uint32, error) {
	if !dp.bound() {
		return 0, errors.New("dap: invalid port binding")
	}
	info, err := dp.validateDPRegister(reg, false)
	if err != nil {
		return 0, err
	}
	if dp.jtag != nil {
		return dp.jtag.readDP(ctx, reg)
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
		value, err = dp.conn.transferDPWriteBarrier(ctx)
	} else {
		value, err = dp.conn.transfer(ctx, dpTransferRequest(reg, true), 0)
	}
	if err != nil {
		return 0, fmt.Errorf("dap: read %s: %w", info.name, err)
	}
	dp.recordDPRead(reg, value)
	return value, nil
}

// WriteDP writes one logical debug-port register. The binding owns
// CTRL/STAT.ORUNDETECT: preserve its SWD value and keep it clear for JTAG.
// JTAG pushed-operation and transaction-counter modes are rejected, as are
// unsupported SWD turnaround settings. Release does
// not restore power-request bits changed through this method. A successful
// DAPABORT write invalidates existing MemAP values. The debug port must be
// connected.
func (dp *DebugPort) WriteDP(ctx context.Context, reg DPRegister, value uint32) error {
	if err := dp.requireOperational(ctx); err != nil {
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
	if dp != nil && dp.jtag != nil {
		return dp.jtag.register(reg, write)
	}
	if reg == IDCODE {
		return dpRegisterInfo{}, errors.New("dap: IDCODE is unavailable on SW-DP")
	}
	info, ok := dp.describeRegister(reg)
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

func (dp *DebugPort) describeRegister(reg DPRegister) (dpRegisterInfo, bool) {
	info, ok := describeDPRegister(reg)
	if reg == DPIDR && dp.reentryID.dpidr.Version == 3 {
		info.bankIndependent = false
	}
	return info, ok
}

func (dp *DebugPort) validateDPWrite(reg DPRegister, value uint32) (dpRegisterInfo, error) {
	info, err := dp.validateDPRegister(reg, true)
	if err != nil {
		return dpRegisterInfo{}, err
	}
	if dp.jtag != nil {
		return info, dp.jtag.validateWrite(reg, value)
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
	if dp.state.session == sessionIdle || !dp.reentryKnown {
		return errors.New("dap: banked DP access requires an active connection")
	}
	if dp.reentryID.dpidr.Version > 3 {
		return fmt.Errorf("dap: banked DP access does not support DPv%d", dp.reentryID.dpidr.Version)
	}
	if dp.reentryID.dpidr.Version < info.minVersion {
		return fmt.Errorf("dap: %s requires DPv%d or later", info.name, info.minVersion)
	}
	return nil
}

func (dp *DebugPort) writeDP(ctx context.Context, reg DPRegister, value uint32) error {
	if !dp.bound() {
		return errors.New("dap: invalid port binding")
	}
	info, err := dp.validateDPWrite(reg, value)
	if err != nil {
		return err
	}
	if dp.jtag != nil {
		return dp.jtag.writeDP(ctx, reg, value)
	}
	if err := dp.prepareDPWrite(ctx, reg, info); err != nil {
		return err
	}
	_, err = dp.conn.transfer(ctx, dpTransferRequest(reg, false), value)
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
	if dpRegisterOffset(reg) != 0 {
		dp.state.settleDPWrite()
	}
	if reg == CTRLSTAT && dp.state.selectDP.valid && dp.state.dpBank() == 0 {
		dp.confirmResponse(value)
	}
}

func (dp *DebugPort) confirmResponse(state uint32) {
	dp.state.confirmResponse(state)
}

func (dp *DebugPort) requireOperational(ctx context.Context) error {
	if ctx == nil {
		return errors.New("dap: nil context")
	}
	if !dp.bound() {
		return errors.New("dap: invalid port binding")
	}
	if dp.state.session == sessionRepairRequired {
		return dp.repairPendingError()
	}
	if dp.state.session != sessionConnected {
		return errors.New("dap: debug port is not connected")
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
