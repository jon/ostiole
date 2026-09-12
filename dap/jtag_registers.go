package dap

import (
	"context"
	"errors"
	"fmt"
)

const jtagTransactionModes = uint32(3<<2 | 0xfff<<12)
const jtagSticky = stickyOverrun | stickyCompare | stickyError

func (e *jtagExecutor) register(reg DPRegister, write bool) (dpRegisterInfo, error) {
	switch reg {
	case IDCODE, CTRLSTAT, SELECT, RDBUFF, ABORT:
	default:
		return dpRegisterInfo{}, fmt.Errorf("dap: %s is unavailable on baseline JTAG-DP", reg)
	}
	info, _ := describeDPRegister(reg)
	info.bankIndependent = true
	if reg == SELECT {
		info.readable = true
	}
	if write && !info.writable || !write && !info.readable {
		return dpRegisterInfo{}, fmt.Errorf("dap: unsupported %s access direction", reg)
	}
	return info, nil
}

func (e *jtagExecutor) validateWrite(reg DPRegister, value uint32) error {
	if reg == ABORT && value != dapAbort {
		return errors.New("dap: baseline JTAG ABORT permits only DAPABORT")
	}
	if reg == CTRLSTAT && value&(jtagTransactionModes|overrunDetect) != 0 {
		return errors.New("dap: CTRL/STAT cannot enable ORUNDETECT, pushed operations, or transaction counting")
	}
	if reg == SELECT && value&0x00ffff0f != 0 {
		return errors.New("dap: baseline JTAG SELECT has no DP register banks")
	}
	return nil
}

func (e *jtagExecutor) readDP(ctx context.Context, reg DPRegister) (uint32, error) {
	if reg == IDCODE {
		return e.idcode(ctx)
	}
	result := e.transfer(ctx, dpTransferRequest(reg, true), 0)
	return result.data, jtagResultError(result)
}

func (e *jtagExecutor) writeDP(ctx context.Context, reg DPRegister, value uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reg == ABORT {
		e.dp.state.invalidateAP()
		e.abortRequired = true
		if err := e.abortPending(ctx); err != nil {
			return err
		}
		return e.prime(ctx)
	}
	result := e.transfer(ctx, dpTransferRequest(reg, false), value)
	if err := jtagResultError(result); err != nil {
		if result.outcome == transferInFlight || result.outcome == transferIndeterminate {
			e.dp.state.loseFraming()
		}
		return err
	}
	if reg == SELECT {
		e.dp.state.recordSELECT(value)
		e.dp.state.confirmSELECT()
	}
	return nil
}
