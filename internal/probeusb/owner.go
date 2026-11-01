// Package probeusb transfers an exact USB attachment into a probe session.
package probeusb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// Session owns USB cleanup and supplies the activated wire.
type Session interface {
	probe.Backend
	probe.Wire
}

// Activate transfers USB into a returned session, even when it returns an
// error. A nil session leaves the USB attachment with the caller.
type Activate func(context.Context, *usb.Device, probe.SWDConfig) (Session, error)

// Open acquires the exact attachment without claiming or configuring it.
func Open(ctx context.Context, identity usb.DeviceInfo, function string, activate Activate) (*probe.Probe, error) {
	return open(ctx, identity, function, func(device attachment) probe.Backend {
		return &owner{device: device, activate: activate}
	})
}

func open(ctx context.Context, identity usb.DeviceInfo, function string, bind func(attachment) probe.Backend) (*probe.Probe, error) {
	if ctx == nil {
		return nil, errors.New("probe: nil open context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	device, err := usb.New().Open(ctx, identity)
	if err != nil {
		return nil, err
	}
	info := probe.Info{Product: identity.Product, Serial: identity.Serial,
		Function: function, Location: fmt.Sprintf("%d:%d", identity.Bus, identity.Address)}
	return probe.New(info, bind(openedUSB{device})), nil
}

type attachment interface {
	Close() error
	raw() *usb.Device
}

type openedUSB struct{ *usb.Device }

func (d openedUSB) raw() *usb.Device { return d.Device }

type owner struct {
	device   attachment
	session  probe.Backend
	activate Activate
}

func (o *owner) SWD(ctx context.Context, config probe.SWDConfig) (probe.Wire, error) {
	session, err := o.activate(ctx, o.device.raw(), config)
	if session != nil {
		o.session = session
		o.device = nil
	}
	return session, err
}

func (o *owner) Close() error {
	var result error
	if o.session != nil {
		err := o.session.Close()
		if err != nil && cleanupPending(o.session) {
			return err
		}
		o.session = nil
		result = err
	}
	if o.device != nil {
		err := o.device.Close()
		if err == nil || !cleanupPending(o.device) {
			o.device = nil
		}
		result = errors.Join(result, err)
	}
	return result
}

func cleanupPending(backend probe.Backend) bool {
	if state, ok := backend.(probe.CleanupState); ok {
		return state.CleanupPending()
	}
	return true
}

func (o *owner) CleanupPending() bool {
	return (o.session != nil && cleanupPending(o.session)) ||
		(o.device != nil && cleanupPending(o.device))
}

func (o *owner) RestorationPossible() bool {
	backend := o.session
	if backend == nil {
		backend = o.device
	}
	if backend == nil {
		return false
	}
	if state, ok := backend.(probe.RestorationState); ok {
		return state.RestorationPossible()
	}
	return true
}

// OpenAttachment acquires an exact attachment without advertising a wire backend.
func OpenAttachment(ctx context.Context, identity usb.DeviceInfo) (*probe.Probe, error) {
	return open(ctx, identity, "", newAttachmentOwner)
}

type attachmentOwner struct{ state owner }

func newAttachmentOwner(device attachment) probe.Backend {
	return &attachmentOwner{state: owner{device: device}}
}

func (o *attachmentOwner) Close() error              { return o.state.Close() }
func (o *attachmentOwner) CleanupPending() bool      { return o.state.CleanupPending() }
func (o *attachmentOwner) RestorationPossible() bool { return o.state.RestorationPossible() }
