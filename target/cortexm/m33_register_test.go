package cortexm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/target/cortexm"
)

func newM33RegisterMemory() *registerMemory {
	m := newRegisterMemory()
	m.cpuid, m.status = 0x411fd210, secureDebug
	return m
}

func TestM33Registers(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		for _, process := range []bool{false, true} {
			checkM33Registers(t, inherited, process)
		}
	}
}

func checkM33Registers(t *testing.T, inherited, process bool) {
	t.Helper()
	m := newM33RegisterMemory()
	m.processStack, m.delay = process, 2
	core := acquireRegisters(t, m, inherited)
	for reg := cortexm.R0; reg <= cortexm.PSP; reg++ {
		index := int(reg) - 1
		if reg == cortexm.SP && process {
			index = 18
		}
		want := m.registers[index]
		got, err := core.ReadRegister(t.Context(), reg)
		if err != nil || got != want {
			t.Fatalf("read %d = %#x, %v; want %#x", reg, got, err, want)
		}
		if reg == cortexm.XPSR {
			continue
		}
		value := uint32(0x20001000)
		if err := core.WriteRegister(t.Context(), reg, value); err != nil {
			t.Fatal(err)
		}
		if m.registers[index] != value {
			t.Fatalf("write %d changed wrong register", reg)
		}
		if err := core.WriteRegister(t.Context(), reg, want); err != nil {
			t.Fatal(err)
		}
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.halted != inherited {
		t.Fatal("register access changed halt ownership")
	}
}

func TestM33RegisterStatusPreservesOwnership(t *testing.T) {
	for _, change := range []string{"restart", "snap", "snap and step", "permission"} {
		t.Run(change, func(t *testing.T) {
			m := newM33RegisterMemory()
			core := acquireRegisters(t, m, false)
			switch change {
			case "restart":
				m.status |= 1 << 26
			case "snap":
				m.control |= 32
			case "snap and step":
				m.control |= 32 | 4
			case "permission":
				m.status = 0
			}
			_, err := core.ReadRegister(t.Context(), cortexm.R4)
			if change == "restart" && err != nil {
				t.Fatal(err)
			}
			if change != "restart" && err == nil {
				t.Fatal("unsafe state accepted")
			}
			writes := m.writes
			m.control &^= 32 | 4
			m.status = secureDebug
			if err := core.Resume(t.Context()); err == nil || m.writes != writes {
				t.Fatal("resumed unowned or unsafe halt")
			}
			err = core.Release(t.Context())
			if change == "permission" {
				if err != nil || m.halted {
					t.Fatal("permission recovery failed", err)
				}
			} else if err == nil || m.writes != writes || !m.halted {
				t.Fatal("unsafe cleanup", err)
			}
		})
	}
}

func TestM33RegisterRestartDuringTransfer(t *testing.T) {
	for _, write := range []bool{false, true} {
		m := newM33RegisterMemory()
		core := acquireRegisters(t, m, false)
		m.afterSelector = func() { m.status |= 1 << 26 }
		var err error
		if write {
			err = core.WriteRegister(t.Context(), cortexm.R4, 42)
		} else {
			_, err = core.ReadRegister(t.Context(), cortexm.R4)
		}
		if err == nil {
			t.Fatal("restart and re-halt accepted as completed transfer")
		}
		if m.transfers != 1 {
			t.Fatal("transfer did not start")
		}
		writes := m.writes
		if err := core.Release(t.Context()); err == nil || m.writes != writes {
			t.Fatal("lost transfer was released")
		}
	}
}

func TestM33RegisterCancellationCleanup(t *testing.T) {
	for _, write := range []bool{false, true} {
		m := newM33RegisterMemory()
		core := acquireRegisters(t, m, false)
		ctx, cancel := context.WithCancel(t.Context())
		m.afterSelector = cancel
		var err error
		if write {
			err = core.WriteRegister(ctx, cortexm.R4, 42)
		} else {
			_, err = core.ReadRegister(ctx, cortexm.R4)
		}
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := core.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if m.transfers != 1 || m.halted || m.control != 0 {
			t.Fatal("cleanup replayed transfer or did not restore control")
		}
	}
}

func TestM33CurrentStateStacks(t *testing.T) {
	for _, nonsecure := range []bool{false, true} {
		for _, process := range []bool{false, true} {
			checkM33Stacks(t, nonsecure, process)
		}
	}
}

func checkM33Stacks(t *testing.T, nonsecure, process bool) {
	t.Helper()
	m := newM33RegisterMemory()
	m.nonsecure, m.processStack = nonsecure, process
	m.nonsecureStacks = [2]uint32{0x20002000, 0x20003000}
	core := acquireRegisters(t, m, true)
	for _, reg := range []cortexm.Register{cortexm.SP, cortexm.MSP, cortexm.PSP} {
		bank := 0
		if reg == cortexm.PSP || reg == cortexm.SP && process {
			bank = 1
		}
		want := m.registers[17+bank]
		if nonsecure {
			want = m.nonsecureStacks[bank]
		}
		got, err := core.ReadRegister(t.Context(), reg)
		if err != nil || got != want {
			t.Fatalf("read %d = %#x, %v; want %#x", reg, got, err, want)
		}
		secureBefore, nonsecureBefore := m.registers, m.nonsecureStacks
		if err := core.WriteRegister(t.Context(), reg, want+4); err != nil {
			t.Fatal(err)
		}
		if nonsecure {
			if m.registers[17] != secureBefore[17] || m.registers[18] != secureBefore[18] || m.nonsecureStacks[bank] != want+4 {
				t.Fatal("write did not preserve Secure stacks")
			}
		} else if m.nonsecureStacks != nonsecureBefore || m.registers[17+bank] != want+4 {
			t.Fatal("write did not preserve Non-secure stacks")
		}
	}
	if err := core.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
