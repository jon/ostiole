package cortexm

import (
	"context"
	"errors"
)

// WriteRegister changes a Cortex-M0 register while halted, without acquiring halt
// ownership. XPSR is read-only. SP, MSP, and PSP require word alignment; PC
// requires bit zero clear and does not change Thumb state. Invalid identifiers
// and values are rejected before traffic. Release does not undo register writes.
//
// The transfer and cleanup rules are those of ReadRegister. An error after
// staging data leaves only Release available; the register may have changed
// if selection was attempted. Release settles that transfer without replaying
// it. Callers own the consequences when execution resumes.
func (t *Target) WriteRegister(ctx context.Context, reg Register, value uint32) error {
	if err := validateRegisterWrite(reg, value); err != nil {
		return err
	}
	if err := t.activeM0(ctx); err != nil {
		return err
	}
	if err := t.waitRegister(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.memory.WriteWord(ctx, dcrdrAddress, value); err != nil {
		t.closing = true
		return err
	}
	if err := t.selectRegister(ctx, uint32(reg-1)|1<<16); err != nil {
		t.closing = true
		return err
	}
	return nil
}

func validateRegisterWrite(reg Register, value uint32) error {
	if reg < R0 || reg > PSP {
		return errors.New("cortexm: invalid register")
	}
	if reg == XPSR {
		return errors.New("cortexm: XPSR is read-only")
	}
	if (reg == SP || reg == MSP || reg == PSP) && value&3 != 0 {
		return errors.New("cortexm: stack pointer must be word-aligned")
	}
	if reg == PC && value&1 != 0 {
		return errors.New("cortexm: PC must have bit zero clear")
	}
	return nil
}
