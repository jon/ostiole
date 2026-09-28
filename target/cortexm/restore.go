package cortexm

import (
	"context"
	"errors"
)

func (t *Target) restore(ctx context.Context) error {
	value, err := t.readDHCSR(ctx)
	if err != nil {
		return err
	}
	if t.saved == 0 && value&cDebugEnable == 0 {
		return nil
	}
	if err := t.checkRestoreState(value); err != nil {
		return err
	}
	if t.haltOwned {
		if err := t.resume(ctx); err != nil {
			return err
		}
	}
	if !t.changed {
		return nil
	}
	value, err = t.readDHCSR(ctx)
	if err != nil {
		return err
	}
	if err := t.validateArchitectureControl(value); err != nil {
		return err
	}
	if value&(cHalt|sHalt) != 0 && value&cDebugEnable != 0 {
		return errors.New("cortexm: new halt prevents restoring disabled debug")
	}
	return t.writeControl(ctx, t.saved)
}

func (t *Target) resume(ctx context.Context) error {
	if t.identity.Part == 0xd21 {
		if _, err := t.readControl(ctx); err != nil {
			return err
		}
		if !t.haltOwned {
			return errors.New("cortexm: halt ownership was lost")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t.resumeUncertain = true
	if err := t.memory.WriteWord(ctx, dhcsrAddress, debugKey|cDebugEnable); err != nil {
		t.closing = true
		return err
	}
	t.haltOwned = false
	if err := t.waitHalt(ctx, false); err != nil {
		t.closing = true
		return err
	}
	return nil
}

func (t *Target) checkRestoreState(value uint32) error {
	if err := t.validateArchitectureControl(value); err != nil {
		return err
	}
	if value&cDebugEnable != 0 && value&(cStep|cMaskInts) != 0 {
		return errors.New("cortexm: cannot restore externally changed stepping or interrupt masking")
	}
	if !t.haltUncertain {
		t.observeHaltRequest(value)
	}
	if t.resumeUncertain || t.haltUncertain {
		if value&(cHalt|sHalt) != 0 {
			return errors.New("cortexm: control write completion is unknown; refusing to resume this halt")
		}
		t.resumeUncertain, t.haltUncertain, t.haltOwned = false, false, false
		t.changed = t.saved&cDebugEnable == 0
	}
	return nil
}
