package armdebug_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type registerBench struct {
	*bench
	registerActivations int
	registerErr         error
	registers           *completedRegisters
}

func (b *registerBench) RegisterSWD(context.Context, probe.SWDConfig) (dap.RegisterTransport, error) {
	b.registerActivations++
	if b.registerErr != nil {
		return nil, b.registerErr
	}
	b.registers = &completedRegisters{Conn: swd.New(b.Wire)}
	return b.registers, nil
}

type completedRegisters struct{ *swd.Conn }

func (r *completedRegisters) Connect(ctx context.Context) (uint32, error) { return r.Conn.Connect(ctx) }
func (r *completedRegisters) ReadDP(ctx context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	value, err := r.Conn.ReadDP(ctx, addr)
	return value, registerOutcome(err), registerCause(err)
}
func (r *completedRegisters) WriteDP(ctx context.Context, addr uint8, value uint32) (dap.RegisterStatus, error) {
	return r.completeWrite(ctx, r.Conn.WriteDP(ctx, addr, value))
}
func (r *completedRegisters) ReadAP(ctx context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	_, err := r.Conn.ReadAP(ctx, addr)
	if err != nil {
		return 0, registerOutcome(err), registerCause(err)
	}
	value, err := r.Conn.ReadDP(ctx, 12)
	if errors.Is(err, swd.ErrFault) {
		return 0, dap.RegisterCompleted, registerCause(err)
	}
	return value, registerOutcome(err), registerCause(err)
}
func (r *completedRegisters) WriteAP(ctx context.Context, addr uint8, value uint32) (dap.RegisterStatus, error) {
	return r.completeWrite(ctx, r.Conn.WriteAP(ctx, addr, value))
}
func (r *completedRegisters) completeWrite(ctx context.Context, err error) (dap.RegisterStatus, error) {
	if err != nil {
		return registerOutcome(err), registerCause(err)
	}
	if _, err := r.Conn.ReadDP(ctx, 12); err != nil {
		return dap.RegisterIndeterminate, registerCause(err)
	}
	return dap.RegisterCompleted, nil
}
func registerOutcome(err error) dap.RegisterStatus {
	switch {
	case err == nil, err == swd.ErrParity:
		return dap.RegisterCompleted
	case err == swd.ErrWait, err == swd.ErrFault:
		return dap.RegisterRejected
	case errors.Is(err, swd.ErrNotExecuted):
		return dap.RegisterUnsent
	default:
		return dap.RegisterIndeterminate
	}
}
func registerCause(err error) error {
	switch {
	case errors.Is(err, swd.ErrWait):
		return errors.Join(dap.ErrWait, err)
	case errors.Is(err, swd.ErrFault):
		return errors.Join(dap.ErrFault, err)
	case errors.Is(err, swd.ErrProtocol):
		return errors.Join(dap.ErrProtocol, err)
	default:
		return err
	}
}

func TestRegisterCapabilityIsPreferredBeforeActivation(t *testing.T) {
	b := &registerBench{bench: newBench()}
	sel := dap.NewAPSel(1)
	if err := b.target.AddMEMAP(sel, 0x24770011, map[uint32]uint32{0x100: 42}); err != nil {
		t.Fatal(err)
	}
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	if b.registerActivations != 1 || b.activations != 0 || b.target.resets != 2 {
		t.Fatalf("activation registers=%d wire=%d resets=%d", b.registerActivations, b.activations, b.target.resets)
	}
	mem, err := c.OpenMemAP(t.Context(), sel)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := mem.ReadWord(t.Context(), 0x100); err != nil || value != 42 {
		t.Fatalf("memory=%d,%v", value, err)
	}
	b.beforeClose = func() {
		state, err := b.target.Read(t.Context(), swdsim.Request{Read: true, Addr: 4})
		if err != nil || state != 0 || b.registers.CleanupPending() {
			t.Fatalf("host close before restoration state=%#x,%v pending=%v", state, err, b.registers.CleanupPending())
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil || b.closes != 1 {
		t.Fatalf("idle close=%v calls=%d", err, b.closes)
	}
}

func TestRegisterActivationErrorNeverFallsBack(t *testing.T) {
	for _, cause := range []error{errors.New("register activation failed"), probe.ErrUnsupportedRegisterSWD} {
		b := &registerBench{bench: newBench(), registerErr: cause}
		c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
		if c != nil || !errors.Is(err, cause) || b.registerActivations != 1 || b.activations != 0 || b.closes != 1 || b.transfers != 0 {
			t.Fatalf("activation owner=%v error=%v registers=%d wire=%d closes=%d transfers=%d", c, err, b.registerActivations, b.activations, b.closes, b.transfers)
		}
	}
}
