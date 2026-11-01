package probe

import (
	"context"
	"errors"
	"reflect"

	"github.com/jon/ostiole/dap"
)

// RegisterSWDBackend configures a completed-register SWD transport, retaining
// all resources for Close even after failure. Success requires a non-nil
// transport; typed nil interfaces are invalid. Protocol entry belongs to
// dap.RegisterTransport.Connect, not activation. Implementing this interface
// advertises the capability before activation; activation errors are failures.
type RegisterSWDBackend interface {
	Backend
	RegisterSWD(context.Context, SWDConfig) (dap.RegisterTransport, error)
}

// ErrUnsupportedRegisterSWD reports absent completed-register SWD capability.
var ErrUnsupportedRegisterSWD = errors.New("probe: register SWD is not supported")

var errInactiveRegisterSWD = errors.New("probe: inactive register SWD transport")

// SupportsRegisterSWD reports the owned backend's completed-register SWD
// capability without I/O. It reports false for empty or closing owners.
// A true result does not guarantee that activation will succeed.
func (p *Probe) SupportsRegisterSWD() bool {
	if p == nil || p.backend == nil || p.closing {
		return false
	}
	_, ok := p.backend.(RegisterSWDBackend)
	return ok
}

// RegisterSWD configures and lends a completed-register SWD transport.
// Activation performs no protocol entry. Failure attempts host cleanup and
// retains pending resources for Close. Only one protocol may be activated;
// reconfiguration requires a fresh owner.
func (p *Probe) RegisterSWD(ctx context.Context, config SWDConfig) (RegisterSWD, error) {
	if err := p.canActivate(ctx, config.MaxClockHz); err != nil {
		return RegisterSWD{}, err
	}
	b, ok := p.backend.(RegisterSWDBackend)
	if !ok {
		return RegisterSWD{}, ErrUnsupportedRegisterSWD
	}
	registers, err := b.RegisterSWD(ctx, config)
	if err := p.activationError(!nilRegisterTransport(registers), err); err != nil {
		return RegisterSWD{}, err
	}
	p.registers = registers
	return RegisterSWD{probe: p}, nil
}

// RegisterSWD borrows completed register operations from its Probe. Give its
// DAP owner exclusive use, release that owner before Probe.Close, and retain
// dependencies after a live cleanup failure. Closing the probe invalidates
// every borrowed call. Its zero value is inactive.
type RegisterSWD struct{ probe *Probe }

func (s RegisterSWD) active() dap.RegisterTransport {
	if s.probe == nil {
		return nil
	}
	return s.probe.registers
}

// Connect enters SWD and returns DPIDR through the active implementation.
func (s RegisterSWD) Connect(ctx context.Context) (uint32, error) {
	if registers := s.active(); registers != nil {
		return registers.Connect(ctx)
	}
	return 0, errInactiveRegisterSWD
}

// Release restores the implementation's protocol state without closing it.
func (s RegisterSWD) Release(ctx context.Context) error {
	if registers := s.active(); registers != nil {
		return registers.Release(ctx)
	}
	return errInactiveRegisterSWD
}

// CleanupPending reports protocol restoration work without I/O. A closed
// borrow reports false; host cleanup is reported by its Probe.
func (s RegisterSWD) CleanupPending() bool {
	if registers := s.active(); registers != nil {
		return registers.CleanupPending()
	}
	return false
}

// ReadDP returns a completed physical DP register value and its outcome.
func (s RegisterSWD) ReadDP(ctx context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	if registers := s.active(); registers != nil {
		return registers.ReadDP(ctx, addr)
	}
	return 0, dap.RegisterUnsent, errInactiveRegisterSWD
}

// WriteDP writes a physical DP register and reports its completion outcome.
func (s RegisterSWD) WriteDP(ctx context.Context, addr uint8, data uint32) (dap.RegisterStatus, error) {
	if registers := s.active(); registers != nil {
		return registers.WriteDP(ctx, addr, data)
	}
	return dap.RegisterUnsent, errInactiveRegisterSWD
}

// ReadAP returns a completed AP value, never a raw posted wire result.
func (s RegisterSWD) ReadAP(ctx context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	if registers := s.active(); registers != nil {
		return registers.ReadAP(ctx, addr)
	}
	return 0, dap.RegisterUnsent, errInactiveRegisterSWD
}

// WriteAP writes an AP register and reports its completion outcome.
func (s RegisterSWD) WriteAP(ctx context.Context, addr uint8, data uint32) (dap.RegisterStatus, error) {
	if registers := s.active(); registers != nil {
		return registers.WriteAP(ctx, addr, data)
	}
	return dap.RegisterUnsent, errInactiveRegisterSWD
}

func nilRegisterTransport(transport dap.RegisterTransport) bool {
	value := reflect.ValueOf(transport)
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
