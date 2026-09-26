package cortexm_test

import (
	"context"
	"errors"
)

const (
	dcrsr         = uint32(0xe000edf4)
	dcrdr         = uint32(0xe000edf8)
	registerReady = uint32(1 << 16)
	resetStatus   = uint32(1 << 25)
)

type registerMemory struct {
	*controlMemory
	registers        [19]uint32
	data, selector   uint32
	pending          bool
	delay, remaining int
	block            bool
	reset            bool
	transfers        int
	beforeStatus     func()
	afterSelector    func()
	processStack     bool
}

func newRegisterMemory() *registerMemory {
	m := &registerMemory{controlMemory: newControlMemory()}
	for i := range m.registers {
		m.registers[i] = 0x12340000 + uint32(i)*4
	}
	m.registers[13] = m.registers[17]
	return m
}

func (m *registerMemory) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if addr == dcrdr {
		m.reads++
		if m.reads == m.failRead {
			return 0, errMemory
		}
		if m.pending || !m.halted {
			return 0, errors.New("data read while unavailable")
		}
		return m.data, nil
	}
	if addr == dhcsr && m.beforeStatus != nil {
		m.beforeStatus()
	}
	value, err := m.controlMemory.ReadWord(ctx, addr)
	if err != nil || addr != dhcsr {
		return value, err
	}
	if m.pending && !m.block {
		if m.remaining == 0 {
			m.complete()
		} else {
			m.remaining--
		}
	}
	if !m.pending {
		value |= registerReady
	}
	if m.reset {
		value |= resetStatus
		m.reset = false
	}
	return value, nil
}

func (m *registerMemory) WriteWord(ctx context.Context, addr, value uint32) error {
	if addr != dcrsr && addr != dcrdr {
		return m.controlMemory.WriteWord(ctx, addr, value)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.writes++
	fail := m.rejectWrites || m.writes == m.failWrite
	if fail && !m.afterWrite {
		return errMemory
	}
	if !m.halted || m.pending {
		return errors.New("register write while unavailable")
	}
	if addr == dcrdr {
		m.data = value
	} else {
		if value&0xffff >= uint32(len(m.registers)) {
			return errors.New("invalid selector")
		}
		m.selector, m.pending, m.remaining = value, true, m.delay
		m.transfers++
		if m.afterSelector != nil {
			m.afterSelector()
		}
	}
	if m.onWrite != nil {
		m.onWrite()
	}
	if fail {
		return errMemory
	}
	return nil
}

func (m *registerMemory) complete() {
	selector := m.selector & 0xffff
	bank := uint32(17)
	if m.processStack {
		bank = 18
	}
	if selector == 13 {
		selector = bank
	}
	if m.selector&(1<<16) != 0 {
		m.registers[selector] = m.data
	} else {
		m.data = m.registers[selector]
	}
	m.registers[13] = m.registers[bank]
	m.pending = false
}
