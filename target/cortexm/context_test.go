package cortexm_test

import (
	"context"
	"testing"
	"time"

	"github.com/jon/ostiole/target/cortexm"
)

type deadlineMemory struct {
	cortexm.Memory
	t   *testing.T
	ctx context.Context
}

func (m *deadlineMemory) check(ctx context.Context) {
	m.t.Helper()
	got, gotOK := ctx.Deadline()
	want, wantOK := m.ctx.Deadline()
	if gotOK != wantOK || !got.Equal(want) {
		m.t.Errorf("memory deadline = %v, %v; want %v, %v", got, gotOK, want, wantOK)
	}
}

func (m *deadlineMemory) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	m.check(ctx)
	return m.Memory.ReadWord(ctx, addr)
}

func (m *deadlineMemory) WriteWord(ctx context.Context, addr, value uint32) error {
	m.check(ctx)
	return m.Memory.WriteWord(ctx, addr, value)
}

func TestOperationsPreserveCallerDeadline(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Second, time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			ctx := t.Context()
			if timeout != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			m := &deadlineMemory{Memory: newRegisterMemory(), t: t, ctx: ctx}
			core, err := cortexm.Acquire(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			operations := []struct {
				name string
				call func(context.Context) error
			}{
				{"Halt", core.Halt},
				{"Halted", func(ctx context.Context) error { _, err := core.Halted(ctx); return err }},
				{"ReadRegister", func(ctx context.Context) error { _, err := core.ReadRegister(ctx, cortexm.R0); return err }},
				{"WriteRegister", func(ctx context.Context) error { return core.WriteRegister(ctx, cortexm.R0, 42) }},
				{"Resume", core.Resume},
				{"Release", core.Release},
			}
			for _, op := range operations {
				t.Run(op.name, func(t *testing.T) {
					m.t = t
					if err := op.call(ctx); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
