package dap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jon/ostiole/swd"
)

const (
	maxWaitRetries      = 100
	waitRecoveryTimeout = time.Second

	dapAbort = uint32(1 << 0)

	clearStickyCompare  = uint32(1 << 1)
	clearStickyError    = uint32(1 << 2)
	clearWriteDataError = uint32(1 << 3)
	clearStickyOverrun  = uint32(1 << 4)

	stickyOverrun  = uint32(1 << 1)
	stickyCompare  = uint32(1 << 4)
	stickyError    = uint32(1 << 5)
	writeDataError = uint32(1 << 7)
)

var errFramingUnknown = errors.New("dap: SWD framing is unknown")

func (dp *DebugPort) transfer(ctx context.Context, req swd.Request, data uint32) (uint32, error) {
	if dp.state.response == responseLost {
		return 0, errFramingUnknown
	}
	waits := 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, dp.stopWaiting(req, waits, err)
		}
		value, err := dp.conn.Transfer(ctx, req, data)
		if err == nil {
			return value, nil
		}
		if !errors.Is(err, swd.ErrWait) {
			return dp.finishRetryError(value, err, waits)
		}
		if err := dp.validateWait(req, err); err != nil {
			return 0, err
		}
		waits++
		if waits > maxWaitRetries {
			cause := fmt.Errorf("dap: WAIT retry limit exceeded: %w", swd.ErrWait)
			return 0, dp.finishWait(req, cause)
		}
	}
}

func (dp *DebugPort) stopWaiting(req swd.Request, waits int, cause error) error {
	if waits == 0 {
		return cause
	}
	return dp.finishWait(req, errors.Join(swd.ErrWait, cause))
}

func (dp *DebugPort) finishRetryError(value uint32, err error, waits int) (uint32, error) {
	if waits == 0 || err == swd.ErrFault {
		return value, err
	}
	cause := errors.Join(swd.ErrWait, fmt.Errorf("dap: WAIT retry failed: %w", err))
	return 0, dp.invalidateWait(cause)
}

func (dp *DebugPort) validateWait(req swd.Request, err error) error {
	if dp.waitForbidden(req) {
		cause := fmt.Errorf("dap: non-stallable request returned WAIT: %w", err)
		if err != swd.ErrWait {
			return dp.invalidateWait(cause)
		}
		return cause
	}
	if err == swd.ErrWait {
		if dp.state.response == responseSimple {
			return nil
		}
		cause := fmt.Errorf("dap: cannot retry WAIT until CTRL/STAT.ORUNDETECT is confirmed clear: %w", err)
		return dp.invalidateWait(cause)
	}
	return dp.invalidateWait(fmt.Errorf("dap: complete WAIT response: %w", err))
}

func (dp *DebugPort) waitForbidden(req swd.Request) bool {
	if req.AP {
		return false
	}
	return req.Read && (req.Addr == uint8(DPIDR) ||
		(req.Addr == uint8(CTRLSTAT) && dp.state.selectDP.valid && dp.state.dpBank() == 0)) ||
		!req.Read && req.Addr == uint8(ABORT)
}

func (dp *DebugPort) finishWait(req swd.Request, cause error) error {
	if !waitMayAffectAP(req) {
		return cause
	}
	return dp.abortWait(cause)
}

func (dp *DebugPort) invalidateWait(cause error) error {
	dp.state.loseFraming()
	return fmt.Errorf("dap: AP state is unknown after incomplete WAIT recovery: %w", cause)
}

func waitMayAffectAP(req swd.Request) bool {
	return req.AP || req.Read && req.Addr == uint8(RDBUFF)
}

func (dp *DebugPort) abortWait(cause error) error {
	dp.state.invalidateAP()
	ctx, cancel := context.WithTimeout(context.Background(), waitRecoveryTimeout)
	defer cancel()

	_, err := dp.conn.Transfer(ctx, swd.Request{}, dapAbort)
	if err != nil {
		dp.state.loseFraming()
		return errors.Join(cause, fmt.Errorf("dap: DAPABORT after extended WAIT: %w", err))
	}
	if err := dp.restoreAfterAbort(ctx); err != nil {
		dp.state.loseFraming()
		return errors.Join(cause, err)
	}
	return cause
}

func (dp *DebugPort) restoreAfterAbort(ctx context.Context) error {
	state, err := dp.conn.Transfer(ctx, swd.Request{Read: true, Addr: uint8(CTRLSTAT)}, 0)
	if err != nil {
		return fmt.Errorf("dap: read sticky state after DAP abort: %w", err)
	}
	dp.state.confirmResponse(state)
	if dp.state.response != responseSimple {
		return errors.New("dap: CTRL/STAT.ORUNDETECT became enabled during DAP abort recovery")
	}
	clear := stickyClearForState(state, dp.identity.Minimal)
	if clear != 0 {
		if _, err := dp.conn.Transfer(ctx, swd.Request{}, clear); err != nil {
			return fmt.Errorf("dap: clear sticky state after DAP abort: %w", err)
		}
	}
	if err := dp.WriteDP(ctx, SELECT, 0); err != nil {
		return fmt.Errorf("dap: restore SELECT after DAP abort: %w", err)
	}
	return nil
}

func supportedStickyClear(minimal bool) uint32 {
	clear := clearStickyError | clearWriteDataError | clearStickyOverrun
	if !minimal {
		clear |= clearStickyCompare
	}
	return clear
}

func stickyClearForState(state uint32, minimal bool) uint32 {
	var clear uint32
	if state&stickyError != 0 {
		clear |= clearStickyError
	}
	if state&writeDataError != 0 {
		clear |= clearWriteDataError
	}
	if state&stickyOverrun != 0 {
		clear |= clearStickyOverrun
	}
	if !minimal && state&stickyCompare != 0 {
		clear |= clearStickyCompare
	}
	return clear
}
