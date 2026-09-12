package probeusb

import (
	"context"

	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// JTAGSession owns USB cleanup and supplies JTAG clocks.
type JTAGSession interface {
	probe.Backend
	probe.JTAGWire
}

// ActivateJTAG transfers USB into a returned session, including on failure.
// A nil session leaves the attachment with the owner.
type ActivateJTAG func(context.Context, *usb.Device, probe.JTAGConfig) (JTAGSession, error)

// OpenProtocols acquires an exact attachment with deferred SWD or JTAG setup.
func OpenProtocols(ctx context.Context, identity usb.DeviceInfo, function string, swd Activate, jtag ActivateJTAG) (*probe.Probe, error) {
	return open(ctx, identity, function, func(device attachment) probe.Backend {
		return &jtagOwner{owner: &owner{device: device, activate: swd}, activateJTAG: jtag}
	})
}

type jtagOwner struct {
	*owner
	activateJTAG ActivateJTAG
}

func (o *jtagOwner) JTAG(ctx context.Context, config probe.JTAGConfig) (probe.JTAGWire, error) {
	session, err := o.activateJTAG(ctx, o.device.raw(), config)
	if session != nil {
		o.session = session
		o.device = nil
	}
	return session, err
}
