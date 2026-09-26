package cortexm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	dhcsrAddress   = uint32(0xe000edf0)
	debugKey       = uint32(0xa05f0000)
	cDebugEnable   = uint32(1)
	cHalt          = uint32(2)
	cStep          = uint32(4)
	cMaskInts      = uint32(8)
	sHalt          = uint32(1 << 17)
	controlTimeout = 5 * time.Second
)

// Memory reads and writes aligned 32-bit target words. A successful write must
// include completion of the underlying memory access, as dap.MemAP does.
// Implementations must honor context cancellation and deadlines.
type Memory interface {
	WordReader
	WriteWord(context.Context, uint32, uint32) error
}

// Target owns Cortex-M0 halting debug state through borrowed memory. Do not copy
// it. Calls and all access to the underlying memory must be serialized. Keep
// exclusive control of the processor's debug registers until Release succeeds,
// then release the memory owner. The zero value is inactive.
type Target struct {
	memory          Memory
	identity        Identity
	saved           uint32
	closing         bool
	changed         bool
	haltOwned       bool
	haltUncertain   bool
	resumeUncertain bool
}

// Acquire enables Cortex-M0 halting debug without requesting a halt. It reads
// CPUID and DHCSR, rejecting other cores, active stepping or interrupt masking,
// and an unfinished halt transition before writing. DHCSR reads consume its
// sticky reset and instruction-retirement indicators.
//
// Calls are bounded to five seconds or the caller's earlier deadline. Failed
// setup attempts restoration with an independent five-second context. A non-nil
// target returned with an error retains cleanup obligations; only Release is
// then available. Memory remains borrowed on every return.
func Acquire(ctx context.Context, memory Memory) (*Target, error) {
	if memory == nil {
		return nil, errors.New("cortexm: nil memory")
	}
	if err := liveContext(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	identity, err := Identify(ctx, memory)
	if err != nil {
		return nil, err
	}
	if identity.Part != 0xc20 || identity.Architecture != 0xc {
		return nil, fmt.Errorf("cortexm: control requires Cortex-M0, got CPUID %#08x", identity.Raw)
	}
	saved, err := memory.ReadWord(ctx, dhcsrAddress)
	if err != nil {
		return nil, err
	}
	if err := validateControl(saved); err != nil {
		return nil, err
	}
	t := &Target{memory: memory, identity: identity, saved: saved & (cDebugEnable | cHalt)}
	if saved&cDebugEnable != 0 {
		return t, nil
	}
	t.saved = 0
	if err := t.writeControl(ctx, cDebugEnable); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), controlTimeout)
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
// cancellation; each attempt is capped at five seconds. Release requires usable
// memory and cannot repair a disconnected or invalidated memory client. It
// never repeats a completed resume. An uncertain control write, or a new halt while
// restoring disabled debug, can prevent cleanup until execution resumes.
func (t *Target) Release(ctx context.Context) error {
	if t == nil || t.memory == nil {
		return nil
	}
	t.closing = true
	if err := liveContext(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
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
	value, err := t.memory.ReadWord(ctx, dhcsrAddress)
	if err != nil {
		return err
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
