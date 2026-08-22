package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

type swdSession struct {
	channel    *ftdi.Channel
	connection *swd.Conn
}

type dapSession struct {
	wire     *swdSession
	port     *dap.DebugPort
	identity dap.DPIDRInfo
	memory   *dap.MemAP
}

func openSWD(ctx context.Context) (*swdSession, error) {
	session, err := openSWDTransport(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := session.connection.Connect(ctx); err != nil {
		return nil, errors.Join(err, session.close())
	}
	return session, nil
}

func openSWDTransport(ctx context.Context) (*swdSession, error) {
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
	channel, err := ftdi.Open(ctx, device, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 400_000})
	if err != nil {
		return nil, errors.Join(err, device.Close())
	}
	connection := swd.New(channel)
	return &swdSession{channel: channel, connection: connection}, nil
}

func (s *swdSession) close() error {
	if s == nil || s.channel == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return errors.Join(s.connection.Release(ctx), s.channel.Close())
}

func openDAP(ctx context.Context) (*dapSession, error) {
	wire, err := openSWDTransport(ctx)
	if err != nil {
		return nil, err
	}
	session := &dapSession{wire: wire, port: dap.NewDebugPort(wire.connection)}
	session.identity, err = session.port.Connect(ctx)
	if err != nil {
		return nil, errors.Join(err, session.close())
	}
	return session, nil
}

func (s *dapSession) close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return errors.Join(s.memory.Release(ctx), s.port.Release(ctx), s.wire.close())
}
