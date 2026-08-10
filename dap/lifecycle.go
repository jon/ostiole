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

// Connect validates the SW-DP and acquires its debug power requests.
//
// The SWD connection must already be in SWD protocol mode. Release restores
// the request bits acquired by this client.
func (dp *DebugPort) Connect(ctx context.Context) (DPIDRInfo, error) {
	if dp == nil || dp.conn == nil {
		return DPIDRInfo{}, errors.New("dap: nil SWD connection")
	}
	if dp.connected || dp.ownedPower != 0 {
		return DPIDRInfo{}, errors.New("dap: SW-DP connection is already active")
	}
	info, state, err := dp.initialize(ctx)
	if err != nil {
		return DPIDRInfo{}, err
	}
	owned := powerRequests &^ state
	if owned != 0 {
		dp.ownedPower = owned
		if err := dp.WriteDP(ctx, CTRLSTAT, state|powerRequests); err != nil {
			return DPIDRInfo{}, dp.rollback(err)
		}
	}
	if err := dp.waitPower(ctx, powerAcks(powerRequests), true); err != nil {
		return DPIDRInfo{}, dp.rollback(err)
	}
	dp.identity = info
	dp.identified = true
	dp.connected = true
	return info, nil
}

func (dp *DebugPort) initialize(ctx context.Context) (DPIDRInfo, uint32, error) {
	dp.overrunDisabled = false
	info, err := dp.identify(ctx)
	if err != nil {
		return DPIDRInfo{}, 0, err
	}
	dp.minimal = info.Minimal
	if err := dp.WriteDP(ctx, ABORT, supportedStickyClear(info.Minimal)); err != nil {
		return DPIDRInfo{}, 0, err
	}
	if err := dp.selectDPBankZero(ctx); err != nil {
		return DPIDRInfo{}, 0, err
	}
	state, err := dp.readSimpleState(ctx)
	if err != nil {
		return DPIDRInfo{}, 0, err
	}
	return info, state, nil
}

func (dp *DebugPort) identify(ctx context.Context) (DPIDRInfo, error) {
	raw, err := dp.ReadDP(ctx, DPIDR)
	if err != nil {
		return DPIDRInfo{}, err
	}
	return DecodeDPIDR(raw)
}

func (dp *DebugPort) readSimpleState(ctx context.Context) (uint32, error) {
	state, err := dp.ReadDP(ctx, CTRLSTAT)
	if err != nil {
		return 0, err
	}
	if state&overrunDetect != 0 {
		return 0, errors.New("dap: CTRL/STAT.ORUNDETECT is enabled; overrun responses are not supported")
	}
	return state, nil
}

func (dp *DebugPort) rollback(cause error) error {
	if dp.ownedPower == 0 {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dp.reenter(ctx); err != nil {
		return errors.Join(cause, fmt.Errorf("dap: restore SWD protocol state: %w", err))
	}
	if err := dp.releasePower(ctx); err != nil {
		return errors.Join(cause, fmt.Errorf("dap: roll back power requests: %w", err))
	}
	return cause
}

// Release restores volatile debug-port state owned by this connection.
func (dp *DebugPort) Release(ctx context.Context) error {
	if dp == nil || dp.conn == nil {
		return nil
	}
	if !dp.connected && dp.ownedPower == 0 && !dp.framingLost {
		return nil
	}
	releaseCtx := ctx
	if dp.framingLost || !dp.overrunDisabled {
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dp.reenter(releaseCtx); err != nil {
			return fmt.Errorf("dap: restore SWD protocol state for release: %w", err)
		}
	}
	if err := dp.WriteDP(releaseCtx, SELECT, 0); err != nil {
		return err
	}
	if err := dp.releasePower(releaseCtx); err != nil {
		return err
	}
	dp.connected = false
	return nil
}

func (dp *DebugPort) reenter(ctx context.Context) error {
	dp.dpBankKnown = false
	if err := dp.conn.JTAGToSWD(ctx); err != nil {
		dp.framingLost = true
		return err
	}
	dp.framingLost = false
	dp.overrunDisabled = false
	info, err := dp.identify(ctx)
	if err != nil {
		dp.framingLost = true
		return fmt.Errorf("dap: identify SW-DP after protocol entry: %w", err)
	}
	dp.minimal = info.Minimal
	if err := dp.WriteDP(ctx, ABORT, supportedStickyClear(info.Minimal)); err != nil {
		dp.framingLost = true
		return fmt.Errorf("dap: clear sticky state after protocol entry: %w", err)
	}
	if err := dp.selectDPBankZero(ctx); err != nil {
		return err
	}
	state, err := dp.ReadDP(ctx, CTRLSTAT)
	if err != nil {
		dp.framingLost = true
		dp.dpBankKnown = false
		return err
	}
	if state&overrunDetect != 0 {
		return errors.New("dap: CTRL/STAT.ORUNDETECT is enabled; overrun responses are not supported")
	}
	return nil
}

func (dp *DebugPort) releasePower(ctx context.Context) error {
	if dp.ownedPower == 0 {
		return nil
	}
	state, err := dp.ReadDP(ctx, CTRLSTAT)
	if err != nil {
		return err
	}
	if err := dp.WriteDP(ctx, CTRLSTAT, state&^dp.ownedPower); err != nil {
		return err
	}
	if err := dp.waitPower(ctx, powerAcks(dp.ownedPower), false); err != nil {
		return err
	}
	dp.ownedPower = 0
	return nil
}

func (dp *DebugPort) waitPower(ctx context.Context, mask uint32, set bool) error {
	for {
		state, err := dp.ReadDP(ctx, CTRLSTAT)
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

// Identity returns the identity established by the most recent connection.
func (dp *DebugPort) Identity() (DPIDRInfo, bool) {
	if dp == nil {
		return DPIDRInfo{}, false
	}
	return dp.identity, dp.identified
}
