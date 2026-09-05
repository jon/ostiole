package ftdi

import (
	"context"
	"errors"

	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// OpenProbe opens one exact USB attachment for a supported MPSSE port. SWD
// activation is deferred until the returned owner's SWD method is called.
func OpenProbe(ctx context.Context, identity usb.DeviceInfo, port Port) (*probe.Probe, error) {
	if !supportedDevice(identity) || (port != PortA && port != PortB) || (identity.PID == PIDFT232H && port != PortA) {
		return nil, errors.New("ftdi: unsupported probe binding")
	}
	function := "A"
	if port == PortB {
		function = "B"
	}
	return probeusb.Open(ctx, identity, function, func(ctx context.Context, device *usb.Device, config probe.SWDConfig) (probeusb.Session, error) {
		session, err := openProbeChannel(ctx, ownedUSBDevice{Device: device}, Config{Port: port, MaxClockHz: config.MaxClockHz})
		if session == nil {
			return nil, err
		}
		return session, err
	})
}

func openProbeChannel(ctx context.Context, device usbDevice, config Config) (*Channel, error) {
	channel, err := newChannel(device, config)
	if err != nil {
		return nil, err
	}
	_, err = prepareChannel(ctx, channel)
	return channel, err
}

func supportedDevice(info usb.DeviceInfo) bool {
	if info.VID != VID {
		return false
	}
	switch info.PID {
	case PIDFT232H, PIDFT2232H, PIDFT4232H:
		return true
	default:
		return false
	}
}
