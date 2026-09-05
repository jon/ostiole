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
	return probe.New(info, &owner{device: openedUSB{device}, activate: activate}), nil
}

type attachment interface {
	Close() error
	raw() *usb.Device
}

type openedUSB struct{ *usb.Device }

func (d openedUSB) raw() *usb.Device { return d.Device }

type owner struct {
	device   attachment
	session  Session
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
	if o.session != nil {
		if err := o.session.Close(); err != nil {
			return err
		}
		o.session = nil
	}
	if o.device != nil {
		if err := o.device.Close(); err != nil {
			return err
		}
		o.device = nil
	}
	return nil
}
