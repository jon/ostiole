package jlink

import (
	"context"
	"errors"

	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// OpenProbe opens one exact J-Link USB attachment. Interface acquisition and
// SWD or JTAG configuration are deferred until the corresponding owner call.
// After a complete scan-status error, the next valid nonempty wire call
// reconfigures the same interface and clock before sending the new scan. The
// failed scan is never replayed.
func OpenProbe(ctx context.Context, identity usb.DeviceInfo) (*probe.Probe, error) {
	if !supportedDevice(identity) {
		return nil, errors.New("jlink: unsupported probe binding")
	}
	swd := func(ctx context.Context, device *usb.Device, config probe.SWDConfig) (probeusb.Session, error) {
		session, err := openProbeSession(ctx, ownedUSBDevice{device}, WithSWD(config.MaxClockHz))
		if session == nil {
			return nil, err
		}
		return session, err
	}
	jtag := func(ctx context.Context, device *usb.Device, config probe.JTAGConfig) (probeusb.JTAGSession, error) {
		session, err := openProbeSession(ctx, ownedUSBDevice{device}, WithJTAG(config.MaxClockHz))
		if session == nil {
			return nil, err
		}
		return session, err
	}
	return probeusb.OpenProtocols(ctx, identity, "", swd, jtag)
}

func openProbeSession(ctx context.Context, device usbDevice, option Option) (*Session, error) {
	session, err := openSession(ctx, device, option)
	if session != nil {
		session.probeOwned = true
	}
	return session, err
}
