package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

type swdSession struct {
	channel    *ftdi.Channel
	connection *swd.Conn
}

func openSWD(ctx context.Context) (*swdSession, error) {
	enumerator := usb.New()
	devices, err := enumerator.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		return nil, err
	}
	if len(devices) != 1 {
		return nil, fmt.Errorf("require exactly one supported FTDI attachment; found %d", len(devices))
	}
	device, err := enumerator.Open(ctx, devices[0])
	if err != nil {
		return nil, err
	}
	channel, err := ftdi.Open(ctx, device, ftdi.Config{
		ClockHz:   400_000,
		ProductID: devices[0].PID,
		Port:      ftdi.PortA,
		Interface: ftdi.SWD,
	})
	if err != nil {
		return nil, err
	}
	connection := swd.New(channel)
	if err := connection.JTAGToSWD(ctx); err != nil {
		return nil, errors.Join(err, channel.Close())
	}
	return &swdSession{channel: channel, connection: connection}, nil
}

func (s *swdSession) close() error {
	if s == nil || s.channel == nil {
		return nil
	}
	return s.channel.Close()
}
