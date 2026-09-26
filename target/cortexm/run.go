package cortexm

import (
	"context"
	"errors"
	"time"
)

// Halt requests a halt and waits for Debug state. A halt already present when
// observed remains unowned. Failure after a write requires Release; the request
// might have taken effect. An unconfirmed write cannot establish ownership of
// an observed halt, so it can prevent cleanup. Halting does not stop peripheral
// clocks or undo instructions executed before the halt.
func (t *Target) Halt(ctx context.Context) error {
	if err := t.active(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	value, err := t.readControl(ctx)
	if err != nil || value&sHalt != 0 {
		return err
	}
	return t.requestHalt(ctx)
}

// Resume releases a halt requested by this target and waits to leave Debug
// state. It refuses inherited halts. Failure can mean execution has already
// resumed; only Release remains available. A new halt is reported without
// resuming it again. An uncertain write leaves cleanup pending while the
// processor remains halted. No instruction effects are undone.
func (t *Target) Resume(ctx context.Context) error {
	if err := t.active(ctx); err != nil {
		return err
	}
	if !t.haltOwned {
		return errors.New("cortexm: no owned halt to resume")
	}
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	if err := t.resume(ctx); err != nil {
		return err
	}
	return nil
}

// Halted reads the current Debug state. It consumes DHCSR's sticky reset and
// instruction-retirement indicators, and does not establish halt ownership.
func (t *Target) Halted(ctx context.Context) (bool, error) {
	if err := t.active(ctx); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	value, err := t.readControl(ctx)
	return value&sHalt != 0, err
}

func (t *Target) active(ctx context.Context) error {
	if t == nil || t.memory == nil || t.closing {
		return errors.New("cortexm: target is unavailable; release may be pending")
	}
	return liveContext(ctx)
}

func (t *Target) readControl(ctx context.Context) (uint32, error) {
	value, err := t.memory.ReadWord(ctx, dhcsrAddress)
	if err == nil && (value&cDebugEnable == 0 || value&(cStep|cMaskInts) != 0) {
		err = errors.New("cortexm: halting debug control changed outside the target")
	}
	if err != nil {
		t.closing = true
	} else {
		t.observeHaltRequest(value)
	}
	return value, err
}

func (t *Target) observeHaltRequest(value uint32) {
	if t.haltOwned && value&cDebugEnable != 0 && value&cHalt == 0 {
		t.haltOwned = false
		t.changed = t.saved&cDebugEnable == 0
	}
}

func (t *Target) requestHalt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.changed = true
	t.haltUncertain = true
	if err := t.memory.WriteWord(ctx, dhcsrAddress, debugKey|cDebugEnable|cHalt); err != nil {
		t.closing = true
		return err
	}
	t.haltUncertain, t.haltOwned = false, true
	if err := t.waitHalt(ctx, true); err != nil {
		t.closing = true
		return err
	}
	return nil
}

func (t *Target) waitHalt(ctx context.Context, halted bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, err := t.readControl(ctx)
		if err != nil {
			return err
		}
		if value&cHalt == 0 && halted {
			return errors.New("cortexm: halt request was not retained")
		}
		if value&cHalt != 0 && !halted {
			return errors.New("cortexm: processor halted again after resume")
		}
		if (value&sHalt != 0) == halted {
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
