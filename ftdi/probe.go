package ftdi

import (
	"context"
	"errors"

	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// OpenProbe opens one exact USB attachment for a supported MPSSE port. SWD
// or JTAG activation is deferred until the corresponding owner method is called.
func OpenProbe(ctx context.Context, identity usb.DeviceInfo, port Port) (*probe.Probe, error) {
	if !supportedDevice(identity) || (port != PortA && port != PortB) || (identity.PID == PIDFT232H && port != PortA) {
		return nil, errors.New("ftdi: unsupported probe binding")
	}
	function := "A"
	if port == PortB {
		function = "B"
	}
	swd := func(ctx context.Context, device *usb.Device, config probe.SWDConfig) (probeusb.Session, error) {
		session, err := Open(ctx, device, Config{Port: port, MaxClockHz: config.MaxClockHz})
		if session == nil {
			return nil, err
		}
		return session, err
	}
	jtag := func(ctx context.Context, device *usb.Device, config probe.JTAGConfig) (probeusb.JTAGSession, error) {
		session, err := Open(ctx, device, Config{Port: port, MaxClockHz: config.MaxClockHz})
		if session == nil {
			return nil, err
		}
		return session, err
	}
	return probeusb.OpenProtocols(ctx, identity, function, swd, jtag)
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
