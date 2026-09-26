package cortexm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

const (
	dhcsr       = uint32(0xe000edf0)
	debugEnable = uint32(1)
	haltRequest = uint32(2)
	haltStatus  = uint32(1 << 17)
)

var errMemory = errors.New("memory failure")

type controlMemory struct {
	cpuid               uint32
	control             uint32
	halted              bool
	reads, writes       int
	failRead, failWrite int
	afterWrite          bool
	ignoreWrites        bool
	onWrite             func()
}

func newControlMemory() *controlMemory { return &controlMemory{cpuid: 0x410cc200} }

func (m *controlMemory) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.reads++
	if m.reads == m.failRead {
		return 0, errMemory
	}
	if addr == 0xe000ed00 {
		return m.cpuid, nil
	}
	if addr != dhcsr {
		return 0, errors.New("unexpected read address")
	}
	value := m.control
	if m.halted {
		value |= haltStatus
	}
	return value, nil
}

func (m *controlMemory) WriteWord(ctx context.Context, addr, value uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if addr != dhcsr || value>>16 != 0xa05f {
		return errors.New("invalid DHCSR write")
	}
	m.writes++
	fail := m.writes == m.failWrite
	if fail && !m.afterWrite {
		return errMemory
	}
	if !m.ignoreWrites {
		m.control = value & 15
		m.halted = m.control&(debugEnable|haltRequest) == debugEnable|haltRequest
	}
	if m.onWrite != nil {
		m.onWrite()
	}
	if fail {
		return errMemory
	}
	return nil
}

func TestAcquireRestoresDebugEnable(t *testing.T) {
	for _, initial := range []uint32{0, debugEnable, debugEnable | haltRequest} {
		t.Run(string(rune('0'+initial)), func(t *testing.T) {
			m := newControlMemory()
			m.control, m.halted = initial, initial&haltRequest != 0
			core, err := cortexm.Acquire(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			if core.Identity().Raw != m.cpuid || m.control&debugEnable == 0 {
				t.Fatalf("identity/control = %#v/%#x", core.Identity(), m.control)
			}
			if m.halted != (initial&haltRequest != 0) {
				t.Fatal("acquisition changed halt state")
			}
			if err := core.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if m.control != initial {
				t.Fatalf("restored control = %#x, want %#x", m.control, initial)
			}
			writes := m.writes
			if err := core.Release(t.Context()); err != nil || m.writes != writes {
				t.Fatal("release was not idempotent")
			}
		})
	}
}

func TestAcquirePreservesEventInducedHalt(t *testing.T) {
	m := newControlMemory()
	m.control, m.halted = debugEnable, true
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.control != debugEnable || !m.halted || m.writes != 0 {
		t.Fatalf("control=%#x halted=%t writes=%d", m.control, m.halted, m.writes)
	}
}

func TestAcquireRejectsUnsupportedStateBeforeWrites(t *testing.T) {
	for _, control := range []uint32{5, 9, 3} {
		m := newControlMemory()
		m.control = control
		if core, err := cortexm.Acquire(t.Context(), m); err == nil || core != nil || m.writes != 0 {
			t.Fatalf("control %#x: core=%v error=%v writes=%d", control, core, err, m.writes)
		}
	}
	m := newControlMemory()
	m.cpuid = 0x410fc241
	if _, err := cortexm.Acquire(t.Context(), m); err == nil || m.writes != 0 {
		t.Fatal("accepted another architecture")
	}
}

func TestAcquireFailureCleanupAndRetry(t *testing.T) {
	for _, after := range []bool{false, true} {
		m := newControlMemory()
		m.failWrite, m.afterWrite = 1, after
		core, err := cortexm.Acquire(t.Context(), m)
		if core != nil || !errors.Is(err, errMemory) || m.control != 0 {
			t.Fatalf("after=%v: core=%v err=%v control=%#x", after, core, err, m.control)
		}
	}
	m := newControlMemory()
	m.failRead, m.failWrite = 3, 2
	core, err := cortexm.Acquire(t.Context(), m)
	if core == nil || !errors.Is(err, errMemory) {
		t.Fatalf("core=%v err=%v", core, err)
	}
	if err := core.Release(t.Context()); err != nil || m.control != 0 {
		t.Fatalf("release = %v; control=%#x", err, m.control)
	}
}

func TestAcquireCleanupOutlivesOperationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := newControlMemory()
	m.onWrite = cancel
	core, err := cortexm.Acquire(ctx, m)
	if core != nil || !errors.Is(err, context.Canceled) || m.control != 0 {
		t.Fatalf("core=%v err=%v control=%#x", core, err, m.control)
	}
}

func TestAcquireIgnoresUnknownDisabledControlBits(t *testing.T) {
	m := newControlMemory()
	m.control = 14
	core, err := cortexm.Acquire(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if m.control != debugEnable {
		t.Fatalf("control = %#x", m.control)
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRequiresLiveContextAndMemory(t *testing.T) {
	m := newControlMemory()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cortexm.Acquire(ctx, m); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var nilContext context.Context
	if _, err := cortexm.Acquire(nilContext, m); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := cortexm.Acquire(t.Context(), nil); err == nil {
		t.Fatal("nil memory accepted")
	}
	if m.reads != 0 || m.writes != 0 {
		t.Fatal("invalid input reached memory")
	}
}

func TestAcquireDetectsIgnoredWrite(t *testing.T) {
	m := newControlMemory()
	m.ignoreWrites = true
	if core, err := cortexm.Acquire(t.Context(), m); err == nil || core != nil {
		t.Fatalf("core=%v err=%v", core, err)
	}
}
