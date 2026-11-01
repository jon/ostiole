package cmsisdap

import (
	"context"

	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// OpenProbe opens one exact USB attachment without claiming a command interface.
// RegisterSWD activation configures the command interface and lends completed
// register operations. Protocol entry belongs to the DAP owner. Release that
// owner before closing the probe. No general SWD wire is advertised.
func OpenProbe(ctx context.Context, identity usb.DeviceInfo) (*probe.Probe, error) {
	return probeusb.OpenRegisterSWD(ctx, identity, "", func(ctx context.Context, device *usb.Device, config probe.SWDConfig) (probeusb.RegisterSession, error) {
		return openRegisterProbeSession(ctx, ownedUSBDevice{Device: device}, config)
	})
}
