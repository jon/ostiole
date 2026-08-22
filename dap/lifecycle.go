package dap

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	overrunDetect = uint32(1 << 0)

	debugPowerRequest  = uint32(1 << 28)
	debugPowerAck      = uint32(1 << 29)
	systemPowerRequest = uint32(1 << 30)
	systemPowerAck     = uint32(1 << 31)

	powerRequests = debugPowerRequest | systemPowerRequest
)

// Connect enters SWD, validates the SW-DP, and acquires its debug power
// requests. The underlying SWD connection establishes its response grammar
// and restores any ORUNDETECT change during Release. If connection setup
// fails, Connect attempts bounded cleanup before returning the original error.
// A cleanup failure is joined to that error; Release may then be retried,
// while other DP, AP, transaction, and MEM-AP operations remain blocked.
func (dp *DebugPort) Connect(ctx context.Context) (DPIDRInfo, error) {
	if dp == nil || dp.conn == nil {
		return DPIDRInfo{}, errors.New("dap: nil SWD connection")
	}
	if dp.state.session == sessionConnected {
		return DPIDRInfo{}, errors.New("dap: SW-DP connection is already active")
	}
	if dp.state.session == sessionRepairRequired {
		return DPIDRInfo{}, errors.New("dap: debug-port cleanup is pending")
	}
	if err := ctx.Err(); err != nil {
		return DPIDRInfo{}, err
	}
	dp.beginConnect()
	raw, err := dp.conn.Connect(ctx)
	if err != nil {
		return DPIDRInfo{}, dp.failSWDConnect(fmt.Errorf("dap: connect SWD transport: %w", err))
	}
	info, state, err := dp.initialize(ctx, raw)
	if err != nil {
		return DPIDRInfo{}, dp.failConnect(err)
	}
	owned := powerRequests &^ state
	if owned != 0 {
		dp.state.ownPower(owned)
		if err := dp.writeDP(ctx, CTRLSTAT, state|powerRequests); err != nil {
			return DPIDRInfo{}, dp.failConnect(err)
		}
	}
	if err := dp.waitPower(ctx, powerAcks(powerRequests), true); err != nil {
		return DPIDRInfo{}, dp.failConnect(err)
	}
	dp.completeConnect(info)
	return info, nil
}

func (dp *DebugPort) failSWDConnect(cause error) error {
	// Release returns nil on an idle connection and checks ctx before repair.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := dp.conn.Release(ctx); err != nil {
		dp.state.loseFraming()
		return cause
	}
	dp.completeRelease()
	return cause
}

func (dp *DebugPort) initialize(ctx context.Context, raw uint32) (DPIDRInfo, uint32, error) {
	info, err := DecodeDPIDR(raw)
	if err != nil {
		return DPIDRInfo{}, 0, err
	}
	dp.reentryID = info
	dp.reentryKnown = true
	dp.state.recordSELECT(0)
	dp.state.confirmSELECT()
	dp.state.settleDPWrite()
	state, err := dp.readResponseState(ctx)
	if err != nil {
		return DPIDRInfo{}, 0, err
	}
	return info, state, nil
}

func (dp *DebugPort) readResponseState(ctx context.Context) (uint32, error) {
	return dp.readDP(ctx, CTRLSTAT)
}

func (dp *DebugPort) failConnect(cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if dp.state.response == responseLost || dp.state.ownedPower != 0 {
		if err := dp.reenter(ctx); err != nil {
			return errors.Join(cause, fmt.Errorf("dap: repair SWD state after Connect failure: %w", err))
		}
	}
	if err := dp.releasePower(ctx); err != nil {
		return errors.Join(cause, fmt.Errorf("dap: roll back power requests: %w", err))
	}
	if err := dp.conn.Release(ctx); err != nil {
		return errors.Join(cause, fmt.Errorf("dap: release SWD transport: %w", err))
	}
	dp.completeRelease()
	return cause
}

// Release restores volatile debug-port state owned by this connection.
//
// Release may be retried. Once an attempt starts, other DP and AP operations
// remain blocked until Release succeeds.
func (dp *DebugPort) Release(ctx context.Context) error {
	if dp == nil || dp.conn == nil {
		return nil
	}
	if dp.state.session == sessionIdle {
		return nil
	}
	dp.state.beginRepair()
	releaseCtx := ctx
	if !dp.state.responseKnown() {
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dp.reenter(releaseCtx); err != nil {
			return fmt.Errorf("dap: restore SWD protocol state for release: %w", err)
		}
	}
	if err := dp.writeDP(releaseCtx, SELECT, 0); err != nil {
		return err
	}
	if _, err := dp.readDP(releaseCtx, RDBUFF); err != nil {
		return fmt.Errorf("dap: confirm SELECT while releasing debug port: %w", err)
	}
	if err := dp.releasePower(releaseCtx); err != nil {
		return err
	}
	if err := dp.conn.Release(releaseCtx); err != nil {
		return fmt.Errorf("dap: release SWD transport: %w", err)
	}
	dp.completeRelease()
	return nil
}

func (dp *DebugPort) reenter(ctx context.Context) error {
	dp.state.beginProtocolEntry()
	raw, err := dp.conn.Connect(ctx)
	if err != nil {
		dp.state.loseFraming()
		return fmt.Errorf("dap: reconnect SWD transport: %w", err)
	}
	info, err := DecodeDPIDR(raw)
	if err != nil {
		dp.state.loseFraming()
		return fmt.Errorf("dap: decode DPIDR after protocol entry: %w", err)
	}
	expected, known := dp.reentryIdentity()
	if known && info.Raw != expected.Raw {
		dp.state.loseFraming()
		return fmt.Errorf("dap: SW-DP identity changed from %#08x to %#08x during protocol entry",
			expected.Raw, info.Raw)
	}
	dp.state.recordSELECT(0)
	dp.state.confirmSELECT()
	dp.state.settleDPWrite()
	_, err = dp.readDP(ctx, CTRLSTAT)
	if err != nil {
		dp.state.loseFraming()
		return err
	}
	return nil
}

func (dp *DebugPort) beginConnect() {
	dp.reentryID = DPIDRInfo{}
	dp.reentryKnown = false
	dp.state.beginConnect()
}

func (dp *DebugPort) completeConnect(info DPIDRInfo) {
	dp.identity = info
	dp.identified = true
	dp.state.completeConnect()
}

func (dp *DebugPort) completeRelease() {
	dp.reentryID = DPIDRInfo{}
	dp.reentryKnown = false
	dp.state.completeRelease()
}

func (dp *DebugPort) reentryIdentity() (DPIDRInfo, bool) {
	return dp.reentryID, dp.reentryKnown
}

func (dp *DebugPort) releasePower(ctx context.Context) error {
	if dp.state.ownedPower == 0 {
		return nil
	}
	state, err := dp.readDP(ctx, CTRLSTAT)
	if err != nil {
		return err
	}
	if err := dp.writeDP(ctx, CTRLSTAT, state&^dp.state.ownedPower); err != nil {
		return err
	}
	if err := dp.waitPower(ctx, powerAcks(dp.state.ownedPower), false); err != nil {
		return err
	}
	dp.state.clearOwnedPower()
	return nil
}

func (dp *DebugPort) waitPower(ctx context.Context, mask uint32, set bool) error {
	for {
		state, err := dp.readDP(ctx, CTRLSTAT)
		if err != nil {
			return err
		}
		if set && state&mask == mask || !set && state&mask == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("dap: wait for power acknowledgement: %w", err)
		}
	}
}

func powerAcks(requests uint32) uint32 {
	var acks uint32
	if requests&debugPowerRequest != 0 {
		acks |= debugPowerAck
	}
	if requests&systemPowerRequest != 0 {
		acks |= systemPowerAck
	}
	return acks
}

// Identity returns the identity established by the most recent successful
// connection. The cached identity remains available after Release and after a
// cleanup failure.
func (dp *DebugPort) Identity() (DPIDRInfo, bool) {
	if dp == nil {
		return DPIDRInfo{}, false
	}
	return dp.identity, dp.identified
}
