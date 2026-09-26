package cortexm

import (
	"context"
	"errors"
)

func (t *Target) restore(ctx context.Context) error {
	value, err := t.memory.ReadWord(ctx, dhcsrAddress)
	if err != nil {
		return err
	}
	if value&cDebugEnable != 0 && value&(cStep|cMaskInts) != 0 {
		return errors.New("cortexm: cannot restore externally changed stepping or interrupt masking")
	}
	if value&(cHalt|sHalt) != 0 && value&cDebugEnable != 0 {
		return errors.New("cortexm: new halt prevents restoring disabled debug")
	}
	return t.writeControl(ctx, t.saved)
}
