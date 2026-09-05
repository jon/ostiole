package cmsisdap

import (
	"context"

	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// OpenProbe opens one exact USB attachment. The owner's SWD call validates
// the CMSIS-DAP v2 command interface and configures SWD before lending a wire.
func OpenProbe(ctx context.Context, identity usb.DeviceInfo) (*probe.Probe, error) {
	return probeusb.Open(ctx, identity, "", func(ctx context.Context, device *usb.Device, config probe.SWDConfig) (probeusb.Session, error) {
		session, err := Open(ctx, device, WithSWD(config.MaxClockHz))
		if session == nil {
			return nil, err
		}
		return session, err
	})
}
