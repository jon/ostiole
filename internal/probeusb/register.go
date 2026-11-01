package probeusb

import (
	"context"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// RegisterSession owns USB cleanup and lends completed register operations.
type RegisterSession interface {
	probe.Backend
	Registers() dap.RegisterTransport
}

// ActivateRegisters transfers USB into a returned session, including on error.
// A nil session leaves the attachment with the owner.
type ActivateRegisters func(context.Context, *usb.Device, probe.SWDConfig) (RegisterSession, error)

// OpenRegisterSWD acquires an exact attachment with deferred register SWD setup.
func OpenRegisterSWD(ctx context.Context, identity usb.DeviceInfo, function string, activate ActivateRegisters) (*probe.Probe, error) {
	return open(ctx, identity, function, func(device attachment) probe.Backend {
		return &registerOwner{state: owner{device: device}, activate: activate}
	})
}

type registerOwner struct {
	state    owner
	activate ActivateRegisters
}

func (o *registerOwner) RegisterSWD(ctx context.Context, config probe.SWDConfig) (dap.RegisterTransport, error) {
	session, err := o.activate(ctx, o.state.device.raw(), config)
	if session == nil {
		return nil, err
	}
	o.state.session = session
	o.state.device = nil
	return session.Registers(), err
}

func (o *registerOwner) Close() error              { return o.state.Close() }
func (o *registerOwner) CleanupPending() bool      { return o.state.CleanupPending() }
func (o *registerOwner) RestorationPossible() bool { return o.state.RestorationPossible() }
