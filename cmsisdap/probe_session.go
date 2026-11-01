package cmsisdap

import (
	"context"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
)

type registerProbeSession struct {
	*Session
	registers *RegisterSWD
}

func (s *registerProbeSession) Registers() dap.RegisterTransport { return s.registers }

func openRegisterProbeSession(ctx context.Context, device usbDevice, config probe.SWDConfig) (probeusb.RegisterSession, error) {
	borrowed := &probeDevice{usbDevice: device}
	session, err := openSession(ctx, borrowed, WithSWD(config.MaxClockHz))
	if session == nil {
		return nil, err
	}
	borrowed.owned = true
	return &registerProbeSession{Session: session, registers: NewRegisterSWD(session)}, err
}

// probeDevice borrows the attachment until activation returns a retained session.
// Before then, the generic probe owner remains responsible for host cleanup.
type probeDevice struct {
	usbDevice
	owned bool
}

func (d *probeDevice) Close() error {
	if !d.owned {
		return nil
	}
	return d.usbDevice.Close()
}

func (d *probeDevice) CleanupPending() bool {
	return d.owned && cleanupPending(d.usbDevice)
}
