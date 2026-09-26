package cortexm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

func TestWriteRegisters(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		for _, delay := range []int{0, 2} {
			t.Run("registers", func(t *testing.T) { writeRegisters(t, inherited, delay) })
		}
	}
}

func writeRegisters(t *testing.T, inherited bool, delay int) {
	t.Helper()
	m := newRegisterMemory()
	m.delay = delay
	core := acquireRegisters(t, m, inherited)
	for reg := cortexm.R0; reg <= cortexm.PSP; reg++ {
		if reg == cortexm.XPSR {
			continue
		}
		value := uint32(0xa5a50000) + uint32(reg)*4
		if err := core.WriteRegister(t.Context(), reg, value); err != nil {
			t.Fatal(err)
		}
		got, err := core.ReadRegister(t.Context(), reg)
		if err != nil || got != value {
			t.Fatalf("register %d: %#x, %v", reg, got, err)
		}
	}
	saved := m.registers
	err := core.Resume(t.Context())
	if (err != nil) != inherited {
		t.Fatalf("resume inherited=%v: %v", inherited, err)
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.registers != saved || m.halted != inherited {
		t.Fatal("release rolled back registers or changed halt ownership")
	}
}

func TestRegisterWriteRejectsBeforeTraffic(t *testing.T) {
	m := newRegisterMemory()
	core := acquireRegisters(t, m, true)
	for _, tc := range []struct {
		reg   cortexm.Register
		value uint32
	}{
		{0, 0}, {255, 0}, {cortexm.XPSR, 0}, {cortexm.PC, 1},
		{cortexm.SP, 1}, {cortexm.MSP, 2}, {cortexm.PSP, 3},
	} {
		reads, writes := m.reads, m.writes
		if err := core.WriteRegister(t.Context(), tc.reg, tc.value); err == nil || reads != m.reads || writes != m.writes {
			t.Fatalf("invalid write reached memory: %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reads, writes := m.reads, m.writes
	if err := core.WriteRegister(ctx, cortexm.R4, 42); !errors.Is(err, context.Canceled) || reads != m.reads || writes != m.writes {
		t.Fatal("canceled write reached memory")
	}
	m.halted = false
	if err := core.WriteRegister(t.Context(), cortexm.R4, 42); err == nil || writes != m.writes {
		t.Fatal("running write reached staging")
	}
	m.halted = true
	if err := core.WriteRegister(t.Context(), cortexm.R4, 42); err != nil {
		t.Fatal(err)
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := core.WriteRegister(t.Context(), cortexm.R4, 42); err == nil {
		t.Fatal("released write")
	}
	var zero cortexm.Target
	if err := zero.WriteRegister(t.Context(), cortexm.R4, 42); err == nil {
		t.Fatal("zero target write")
	}
}

func TestRegisterWriteFailures(t *testing.T) {
	for _, phase := range []string{"stage-before", "stage-after", "select-before", "select-after", "poll", "cancel-stage", "cancel-select"} {
		t.Run(phase, func(t *testing.T) {
			m := newRegisterMemory()
			core := acquireRegisters(t, m, false)
			old := m.registers[4]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := errMemory
			switch phase {
			case "stage-before", "stage-after":
				m.failWrite = m.writes + 1
				m.afterWrite = phase == "stage-after"
			case "select-before", "select-after":
				m.failWrite = m.writes + 2
				m.afterWrite = phase == "select-after"
			case "poll":
				m.failRead = m.reads + 2
			case "cancel-stage":
				m.onWrite = cancel
				want = context.Canceled
			case "cancel-select":
				m.afterSelector = cancel
				want = context.Canceled
			}
			if err := core.WriteRegister(ctx, cortexm.R4, 42); !errors.Is(err, want) {
				t.Fatal(err)
			}
			if err := core.Halt(t.Context()); err == nil {
				t.Fatal("ordinary call after uncertain write")
			}
			m.onWrite, m.afterSelector = nil, nil
			transfers := m.transfers
			if err := core.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			expected := old
			if phase == "select-after" || phase == "poll" || phase == "cancel-select" {
				expected = 42
			}
			if m.registers[4] != expected || m.transfers != transfers || m.halted {
				t.Fatal("wrong write effect, replay, or failed resume")
			}
		})
	}
}

func TestRegisterWritePendingCleanup(t *testing.T) {
	m := newRegisterMemory()
	core := acquireRegisters(t, m, false)
	m.block = true
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	err := core.WriteRegister(ctx, cortexm.R4, 42)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	writes := m.writes
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Millisecond)
	err = core.Release(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || m.writes != writes || !m.halted {
		t.Fatal("pending write resumed")
	}
	m.block = false
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.registers[4] != 42 || m.halted || m.transfers != 1 {
		t.Fatal("write failed or replayed")
	}
}

func TestRegisterStackBanks(t *testing.T) {
	for _, process := range []bool{false, true} {
		m := newRegisterMemory()
		m.processStack = process
		core := acquireRegisters(t, m, true)
		if err := core.WriteRegister(t.Context(), cortexm.MSP, 0x20001000); err != nil {
			t.Fatal(err)
		}
		if err := core.WriteRegister(t.Context(), cortexm.PSP, 0x20002000); err != nil {
			t.Fatal(err)
		}
		want := uint32(0x20001000)
		bank, other := cortexm.MSP, cortexm.PSP
		otherWant := uint32(0x20002000)
		if process {
			want, otherWant = otherWant, want
			bank, other = other, bank
		}
		got, err := core.ReadRegister(t.Context(), cortexm.SP)
		if err != nil || got != want {
			t.Fatalf("SP=%#x err=%v", got, err)
		}
		if err := core.WriteRegister(t.Context(), cortexm.SP, 0x20003000); err != nil {
			t.Fatal(err)
		}
		got, err = core.ReadRegister(t.Context(), bank)
		if err != nil || got != 0x20003000 {
			t.Fatalf("active SP=%#x err=%v", got, err)
		}
		got, err = core.ReadRegister(t.Context(), other)
		if err != nil || got != otherWant {
			t.Fatalf("inactive SP=%#x err=%v", got, err)
		}
		if err := core.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegisterWriteObservesLostControl(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		m := newRegisterMemory()
		core := acquireRegisters(t, m, false)
		m.control, m.halted = debugEnable, false
		if disabled {
			m.control = 0
		}
		if err := core.WriteRegister(t.Context(), cortexm.R4, 42); err == nil {
			t.Fatal("lost control accepted")
		}
		m.control, m.halted = debugEnable|haltRequest, true
		writes := m.writes
		if err := core.Resume(t.Context()); err == nil || m.writes != writes {
			t.Fatal("resumed independent stop")
		}
		if disabled {
			if err := core.WriteRegister(t.Context(), cortexm.R4, 42); err == nil {
				t.Fatal("ordinary call after disabled debug")
			}
		}
	}
}
