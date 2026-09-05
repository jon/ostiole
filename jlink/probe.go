package jlink

import (
	"context"
	"errors"

	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// OpenProbe opens one exact J-Link USB attachment. Interface acquisition and
// SWD configuration are deferred until the returned owner's SWD call.
func OpenProbe(ctx context.Context, identity usb.DeviceInfo) (*probe.Probe, error) {
	if !supportedDevice(identity) {
		return nil, errors.New("jlink: unsupported probe binding")
	}
	return probeusb.Open(ctx, identity, "", func(ctx context.Context, device *usb.Device, config probe.SWDConfig) (probeusb.Session, error) {
		session, err := Open(ctx, device, WithSWD(config.MaxClockHz))
		if session == nil {
			return nil, err
		}
		return session, err
	})
}
