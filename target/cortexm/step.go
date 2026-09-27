package cortexm

import (
	"context"
	"errors"
	"time"
)

type stepPhase uint8

const (
	stepIdle stepPhase = iota
	stepUncertain
	stepRunning
	stepStopped
	stepRestoring
	stepLost
	dfsrAddress = uint32(0xe000ed30)
)

// Step performs one architectural step from a halt owned by this target, then
// returns halted with stepping disabled. Exceptions can be taken and debug
// events can interrupt a step; success does not promise instruction retirement.
// Interrupt masking is unchanged. Existing competing DFSR event flags prevent
// stepping, and none of its flags are cleared.
//
// The caller controls cancellation and deadlines. Once launch is attempted,
// any failure leaves only Release available. Release never repeats a step and
// only clears stepping while halted. Unconfirmed launch, reset, or lost debug
// control can prevent automatic cleanup. A competing halt is not owned
// and can prevent restoring initially disabled debug. Execution is not undone.
func (t *Target) Step(ctx context.Context) error {
	if err := t.active(ctx); err != nil {
		return err
	}
	if !t.haltOwned {
		return errors.New("cortexm: no owned halt to step")
	}
	if err := t.waitRegister(ctx); err != nil {
		return err
	}
	if !t.haltOwned {
		return errors.New("cortexm: halt ownership was lost")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reason, err := t.memory.ReadWord(ctx, dfsrAddress)
	if err != nil {
		t.closing = true
		return err
	}
	if reason&0x1e != 0 {
		return errors.New("cortexm: competing debug event flags prevent stepping")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t.step = stepUncertain
	t.haltOwned = false
	if err := t.memory.WriteWord(ctx, dhcsrAddress, debugKey|cDebugEnable|cStep); err != nil {
		t.closing = true
		return err
	}
	t.step = stepRunning
	if err := t.settleStep(ctx); err != nil {
		t.closing = true
		return err
	}
	if !t.haltOwned {
		t.closing = true
		return errors.New("cortexm: step stopped on an unowned debug event")
	}
	return nil
}

func (t *Target) settleStep(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, err := t.stepStatus(ctx)
		if err != nil {
			return err
		}
		if t.step == stepRestoring && value&15 == cDebugEnable|cHalt && value&sHalt != 0 {
			t.step = stepIdle
			return nil
		}
		if value&(cHalt|sHalt) == cHalt|sHalt {
			if t.step == stepRunning {
				t.step = stepStopped
			}
			if err := t.finishStep(ctx); err != nil {
				return err
			}
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

func (t *Target) stepStatus(ctx context.Context) (uint32, error) {
	if t.step == stepUncertain || t.step == stepLost {
		return 0, errors.New("cortexm: step completion is unknown; cleanup cannot continue")
	}
	value, err := t.memory.ReadWord(ctx, dhcsrAddress)
	if err != nil {
		return 0, err
	}
	invalid := value&cDebugEnable == 0 || value&(cMaskInts|sReset) != 0
	if t.step != stepRestoring {
		invalid = invalid || value&cStep == 0
	}
	if t.step == stepStopped || t.step == stepRestoring {
		invalid = invalid || value&(cHalt|sHalt) != cHalt|sHalt
	}
	if invalid {
		t.step = stepLost
		return 0, errors.New("cortexm: debug control changed during step")
	}
	return value, nil
}

func (t *Target) finishStep(ctx context.Context) error {
	if t.step == stepStopped {
		reason, err := t.memory.ReadWord(ctx, dfsrAddress)
		if err != nil {
			return err
		}
		t.haltOwned = reason&0x1f == 1
		if !t.haltOwned {
			t.changed = t.saved&cDebugEnable == 0
		}
		t.step = stepRestoring
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return t.memory.WriteWord(ctx, dhcsrAddress, debugKey|cDebugEnable|cHalt)
}
