package cortexm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	dhcsrAddress          = uint32(0xe000edf0)
	debugKey              = uint32(0xa05f0000)
	cDebugEnable          = uint32(1)
	cHalt                 = uint32(2)
	cStep                 = uint32(4)
	cMaskInts             = uint32(8)
	sHalt                 = uint32(1 << 17)
	acquireCleanupTimeout = 5 * time.Second
)

// Memory reads and writes aligned 32-bit target words. A successful write must
// include completion of the underlying memory access, as dap.MemAP does.
// Implementations must honor context cancellation and deadlines.
type Memory interface {
	WordReader
	WriteWord(context.Context, uint32, uint32) error
}

// Target owns one Cortex-M0 or Cortex-M33 processor's halting debug through
// borrowed memory.
// Do not copy it. Calls and all access to the underlying memory must be serialized. Keep
// exclusive control of the processor's debug registers until Release succeeds,
// then release the memory owner. The caller controls operation cancellation
// and deadlines. The zero value is inactive.
type Target struct {
	memory          Memory
	identity        Identity
	saved           uint32
	closing         bool
	changed         bool
	haltOwned       bool
	haltUncertain   bool
	resumeUncertain bool
	registerPending bool
	registerLost    bool
	snapStalled     bool
	step            stepPhase
}

// Acquire enables Cortex-M0 or Cortex-M33 halting debug without requesting a
// halt. It reads CPUID and DHCSR, rejecting other cores, active stepping or
// interrupt masking, and an unfinished halt transition before writing.
// Cortex-M33 requires Secure invasive debug permission (S_SDE) and rejects
// snap-stall state. It does not change authentication or security settings.
// DHCSR reads consume sticky reset, retirement, and Cortex-M33 restart status.
// Stepping currently requires Cortex-M0.
//
// The caller controls cancellation and deadlines. Failed setup attempts
// restoration with an independent five-second context. A non-nil target
// returned with an error retains cleanup obligations; only Release is then
// available. Memory remains borrowed on every return.
func Acquire(ctx context.Context, memory Memory) (*Target, error) {
	if memory == nil {
		return nil, errors.New("cortexm: nil memory")
	}
	if err := liveContext(ctx); err != nil {
		return nil, err
	}
	identity, err := Identify(ctx, memory)
	if err != nil {
		return nil, err
	}
	if err := controlIdentity(identity); err != nil {
		return nil, err
	}
	saved, err := memory.ReadWord(ctx, dhcsrAddress)
	if err != nil {
		return nil, err
	}
	if err := validateControl(saved); err != nil {
		return nil, err
	}
	t := &Target{memory: memory, identity: identity, saved: saved & (cDebugEnable | cHalt)}
	if err := t.validateArchitectureControl(saved); err != nil {
		return nil, err
	}
	if saved&cDebugEnable != 0 {
		return t, nil
	}
	t.saved = 0
	if err := t.writeControl(ctx, cDebugEnable); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), acquireCleanupTimeout)
		defer cancel()
		if releaseErr := t.Release(cleanup); releaseErr != nil {
			return t, errors.Join(err, releaseErr)
		}
		return nil, err
	}
	return t, nil
}

func validateControl(value uint32) error {
	if value&cDebugEnable == 0 {
		return nil
	}
	if value&(cStep|cMaskInts) != 0 {
		return errors.New("cortexm: inherited stepping or interrupt masking is unsupported")
	}
	if value&cHalt != 0 && value&sHalt == 0 {
		return errors.New("cortexm: halt transition is incomplete")
	}
	return nil
}

// Identity returns the acquired CPUID, including after release.
func (t *Target) Identity() Identity {
	if t == nil {
		return Identity{}
	}
	return t.identity
}

// Release restores the inherited debug control. A target initially halted
// remains halted. Failed restoration is retryable and blocks ordinary calls.
// Nil and released targets need no cleanup. Use a fresh context after operation
// cancellation and choose its deadline. Release requires usable memory and
// cannot repair a disconnected or invalidated memory client. It never repeats
// a completed resume. An unconfirmed control change, or a new halt while
// restoring disabled debug, can prevent cleanup until execution resumes.
// Pending register transfers must settle first. Reset or loss of Debug state
// or Cortex-M33 restart during a transfer prevents automatic cleanup.
// An accepted step must return halted before stepping can be disabled;
// an unconfirmed step launch prevents
// automatic cleanup. A competing debug event leaves its halt unowned.
// Observed Cortex-M33 snap-stall state permanently prevents automatic resume;
// clearing its control bit does not make the memory system safe to resume.
func (t *Target) Release(ctx context.Context) error {
	if t == nil || t.memory == nil {
		return nil
	}
	t.closing = true
	if err := liveContext(ctx); err != nil {
		return err
	}
	if t.registerPending {
		if err := t.waitRegister(ctx); err != nil {
			return err
		}
	}
	if t.step != stepIdle {
		if err := t.settleStep(ctx); err != nil {
			return err
		}
	}
	if t.changed {
		if err := t.restore(ctx); err != nil {
			return fmt.Errorf("cortexm: restore debug control: %w", err)
		}
	}
	t.memory = nil
	return nil
}

func (t *Target) writeControl(ctx context.Context, control uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.changed = true
	if err := t.memory.WriteWord(ctx, dhcsrAddress, debugKey|control); err != nil {
		return err
	}
	value, err := t.readDHCSR(ctx)
	if err != nil {
		return err
	}
	if value&cDebugEnable == 0 && t.saved == 0 {
		t.changed = false
	}
	if control&cDebugEnable != 0 {
		if err := t.validateArchitectureControl(value); err != nil {
			return err
		}
	}
	mask := cDebugEnable
	if control&cDebugEnable != 0 {
		mask |= cHalt | cStep | cMaskInts
	}
	if value&mask != control&mask {
		return fmt.Errorf("cortexm: DHCSR control %#x, want %#x", value&mask, control&mask)
	}
	return nil
}

func liveContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("cortexm: nil context")
	}
	return ctx.Err()
}
