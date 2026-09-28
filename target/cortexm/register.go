package cortexm

import (
	"context"
	"errors"
	"time"
)

// Register identifies a Cortex-M0 core register. Zero and unnamed values are
// invalid. The numeric values are not hardware register selectors.
type Register uint8

// Core registers accessible through an acquired, halted target. SP is the
// current stack pointer; MSP and PSP select its banks explicitly. PC is the
// debug return address, not a Thumb function pointer. XPSR includes status.
const (
	R0 Register = iota + 1
	R1
	R2
	R3
	R4
	R5
	R6
	R7
	R8
	R9
	R10
	R11
	R12
	SP
	LR
	PC
	XPSR
	MSP
	PSP
)

const (
	dcrsrAddress = uint32(0xe000edf4)
	dcrdrAddress = uint32(0xe000edf8)
	sRegReady    = uint32(1 << 16)
	sReset       = uint32(1 << 25)
)

// ReadRegister reads a Cortex-M0 register while halted, without acquiring halt
// ownership. It writes debug transfer registers and consumes DHCSR's sticky status. The
// caller controls cancellation and deadlines. An uncertain transfer blocks
// ordinary calls; Release must settle it before changing debug control. Loss
// of Debug state or reset during a pending transfer prevents automatic cleanup.
// An error returns no valid register value.
func (t *Target) ReadRegister(ctx context.Context, reg Register) (uint32, error) {
	if reg < R0 || reg > PSP {
		return 0, errors.New("cortexm: invalid register")
	}
	if err := t.activeM0(ctx); err != nil {
		return 0, err
	}
	if err := t.waitRegister(ctx); err != nil {
		return 0, err
	}
	if err := t.selectRegister(ctx, uint32(reg-1)); err != nil {
		return 0, err
	}
	value, err := t.memory.ReadWord(ctx, dcrdrAddress)
	if err != nil {
		t.closing = true
		return 0, err
	}
	return value, nil
}

func (t *Target) selectRegister(ctx context.Context, selector uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.registerPending = true
	if err := t.memory.WriteWord(ctx, dcrsrAddress, selector); err != nil {
		t.closing = true
		return err
	}
	return t.waitRegister(ctx)
}

func (t *Target) waitRegister(ctx context.Context) (err error) {
	defer func() {
		if err != nil && t.registerPending {
			t.closing = true
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := t.registerStatus(ctx); err != nil {
			return err
		}
		if !t.registerPending {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (t *Target) registerStatus(ctx context.Context) error {
	if t.registerLost {
		return errors.New("cortexm: register transfer lost its debug state; cleanup cannot continue")
	}
	value, err := t.memory.ReadWord(ctx, dhcsrAddress)
	if err != nil {
		t.closing = true
		return err
	}
	halted := value&(cDebugEnable|sHalt) == cDebugEnable|sHalt
	if t.registerPending && (!halted || value&sReset != 0) {
		t.registerLost = true
		return errors.New("cortexm: debug state changed during register transfer")
	}
	if value&cDebugEnable == 0 || value&(cStep|cMaskInts) != 0 {
		t.closing = true
		return errors.New("cortexm: debug mode changed during register access")
	}
	t.observeHaltRequest(value)
	if !halted {
		return errors.New("cortexm: register access requires a halted processor")
	}
	t.registerPending = value&sRegReady == 0
	return nil
}
