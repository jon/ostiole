package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

func (e *swdExecutor) readAP(ctx context.Context, sel APSel, addr uint16, preserveAP bool) (bool, uint32, error) {
	if e.register != nil {
		value, err := e.transferRegister(ctx, apTransferRequest(uint8(addr&12), true), 0, preserveAP)
		if err != nil {
			err = fmt.Errorf("dap: read %s register %#x: %w", sel, addr, err)
		}
		return !requestWasNotSent(err) && !requestWasRejected(err), value, err
	}
	_, err := e.transfer(ctx, apTransferRequest(uint8(addr&0x0c), true), 0)
	if err != nil {
		possible := !requestWasRejected(err) && !requestWasNotSent(err) && !errors.Is(err, swd.ErrFault)
		if possible && errors.Is(err, swd.ErrParity) {
			err = errors.Join(err, ErrIndeterminate)
		}
		return possible, 0, fmt.Errorf("dap: post %s register %#x read: %w", sel, addr, err)
	}
	e.pendingRead = true
	defer func() { e.pendingRead = false }()
	value, err := e.dp.readDP(ctx, RDBUFF)
	if err != nil {
		err = fmt.Errorf("dap: complete %s register %#x read: %w", sel, addr, err)
	}
	return true, value, err
}

func (e *swdExecutor) writeAP(ctx context.Context, sel APSel, addr uint16, value uint32, preserveAP bool) (bool, error) {
	if e.register != nil {
		_, err := e.transferRegister(ctx, apTransferRequest(uint8(addr&12), false), value, preserveAP)
		if err != nil {
			err = fmt.Errorf("dap: write %s register %#x: %w", sel, addr, err)
		}
		return !requestWasNotSent(err) && !requestWasRejected(err), err
	}
	_, err := e.transfer(ctx, apTransferRequest(uint8(addr&0x0c), false), value)
	if err != nil {
		possible := !requestWasRejected(err) && !requestWasNotSent(err) && !errors.Is(err, swd.ErrFault)
		if possible {
			err = errors.Join(err, ErrIndeterminate)
		}
		return possible, fmt.Errorf("dap: write %s register %#x: %w", sel, addr, err)
	}
	if _, err := e.dp.readDP(ctx, RDBUFF); err != nil {
		rejected := faultReportsWriteDataError(err)
		if !rejected && !confirmedSWDCompletionParity(err) {
			err = errors.Join(err, ErrIndeterminate)
		}
		return !rejected, fmt.Errorf("dap: complete %s register %#x write: %w", sel, addr, err)
	}
	return true, nil
}

// A joined parity error can come from failed recovery after FAULT or WAIT;
// only a parity error on the completion response itself confirms the write.
func confirmedSWDCompletionParity(err error) bool {
	for err != nil {
		if err == swd.ErrParity {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}
