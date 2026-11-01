package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

func (e *swdExecutor) readAP(ctx context.Context, addr uint8) (bool, uint32, error) {
	_, err := e.transfer(ctx, apTransferRequest(addr&0x0c, true), 0)
	if err != nil {
		possible := !requestWasRejected(err) && !requestWasNotSent(err) && !errors.Is(err, swd.ErrFault)
		return possible, 0, fmt.Errorf("dap: post raw AP read at %#02x: %w", addr, err)
	}
	value, err := e.dp.readDP(ctx, RDBUFF)
	return true, value, err
}

func (e *swdExecutor) writeAP(ctx context.Context, addr uint8, value uint32) (bool, error) {
	_, err := e.transfer(ctx, apTransferRequest(addr&0x0c, false), value)
	if err != nil {
		possible := !requestWasRejected(err) && !requestWasNotSent(err) && !errors.Is(err, swd.ErrFault)
		if possible {
			err = errors.Join(err, ErrIndeterminate)
		}
		return possible, fmt.Errorf("dap: write raw AP register at %#02x: %w", addr, err)
	}
	if _, err := e.dp.readDP(ctx, RDBUFF); err != nil {
		rejected := faultReportsWriteDataError(err)
		if !rejected && !confirmedSWDCompletionParity(err) {
			err = errors.Join(err, ErrIndeterminate)
		}
		return !rejected, fmt.Errorf("dap: complete raw AP write at %#02x: %w", addr, err)
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
