package cortexm

import (
	"context"
	"errors"
	"fmt"
)

const (
	cSnapStall   = uint32(1 << 5)
	sSecureDebug = uint32(1 << 20)
	sRestart     = uint32(1 << 26)
)

func controlIdentity(identity Identity) error {
	if identity.Part == 0xc20 && identity.Architecture == 0xc ||
		identity.Part == 0xd21 && identity.Architecture == 0xf {
		return nil
	}
	return fmt.Errorf("cortexm: control requires Cortex-M0 or Cortex-M33, got CPUID %#08x", identity.Raw)
}

func (t *Target) validateArchitectureControl(value uint32) error {
	if t.identity.Part != 0xd21 {
		return nil
	}
	if value&cSnapStall != 0 {
		t.snapStalled = true
	}
	if t.snapStalled {
		return errors.New("cortexm: snap-stall state requires system reset before resuming")
	}
	if value&sSecureDebug == 0 {
		return errors.New("cortexm: Cortex-M33 control requires Secure invasive debug permission")
	}
	return nil
}

func (t *Target) readDHCSR(ctx context.Context) (uint32, error) {
	value, err := t.memory.ReadWord(ctx, dhcsrAddress)
	if err == nil && t.identity.Part == 0xd21 {
		t.snapStalled = t.snapStalled || value&cSnapStall != 0
		if value&(cDebugEnable|sRestart) == cDebugEnable|sRestart && (t.haltOwned || t.resumeUncertain) {
			t.haltOwned, t.resumeUncertain = false, false
			t.changed = t.saved&cDebugEnable == 0
		}
	}
	return value, err
}
