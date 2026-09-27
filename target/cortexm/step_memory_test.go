package cortexm_test

import (
	"context"
	"errors"
)

const stepRequest = uint32(4)

type stepMemory struct {
	*registerMemory
	reasons                  uint32
	stepping                 bool
	stepDelay, stepRemaining int
	blockStep                bool
	launches, steps          int
	onStep                   func()
}

func newStepMemory() *stepMemory { return &stepMemory{registerMemory: newRegisterMemory(), reasons: 1} }

func (m *stepMemory) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	if addr == 0xe000ed30 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		m.reads++
		if m.reads == m.failRead {
			return 0, errMemory
		}
		return m.reasons, nil
	}
	if ctx.Err() == nil && addr == dhcsr && m.stepping && !m.blockStep {
		if m.stepRemaining == 0 {
			m.finishStep()
		} else {
			m.stepRemaining--
		}
	}
	return m.registerMemory.ReadWord(ctx, addr)
}

func (m *stepMemory) WriteWord(ctx context.Context, addr, value uint32) error {
	if addr != dhcsr {
		return m.registerMemory.WriteWord(ctx, addr, value)
	}
	if !m.halted && m.control&debugEnable != 0 && (m.control^value)&12 != 0 {
		return errors.New("changed step/mask control while running")
	}
	wasHalted, writes := m.halted, m.writes
	err := m.registerMemory.WriteWord(ctx, addr, value)
	if wasHalted && value&15 == debugEnable|stepRequest && m.writes != writes && m.control == debugEnable|stepRequest && !m.ignoreWrites {
		m.stepping, m.stepRemaining = true, m.stepDelay
		m.launches++
		if m.stepDelay == 0 && !m.blockStep {
			m.finishStep()
		}
	}
	return err
}

func (m *stepMemory) finishStep() {
	m.stepping = false
	m.steps++
	m.registers[0]++
	m.registers[15] += 2
	m.control |= haltRequest
	m.halted = true
	if m.onStep != nil {
		m.onStep()
	}
}
