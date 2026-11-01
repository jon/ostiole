package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/internal/command"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

const (
	transferTimeout = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	signalCtx, stop := command.SignalContext()
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, transferTimeout)
	defer cancel()
	dpidr, err := readDPIDR(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("DPIDR=%#08x\n", dpidr)
	return nil
}

func readDPIDR(ctx context.Context) (value uint32, err error) {
	bus := usb.New()
	devs, err := bus.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		return 0, err
	}
	if len(devs) != 1 {
		return 0, fmt.Errorf("ostiole: require exactly one supported FTDI attachment; found %d", len(devs))
	}
	dev, err := bus.Open(ctx, devs[0])
	if err != nil {
		return 0, err
	}
	ch, err := ftdi.Open(ctx, dev, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 1_000_000})
	if err != nil {
		closeOwner := dev.Close
		if ch != nil {
			closeOwner = ch.Close
		}
		return 0, errors.Join(err, closeOwner())
	}
	conn := swd.New(ch)
	defer func() { err = errors.Join(err, releaseConnection(conn, ch)) }()
	value, err = conn.Connect(ctx)
	if err != nil {
		return 0, err
	}
	if value == 0 || value == math.MaxUint32 || value&1 == 0 {
		return 0, fmt.Errorf("ostiole: invalid DPIDR %#08x", value)
	}
	return value, nil
}

type protocolRelease interface {
	Release(context.Context) error
	CleanupPending() bool
}

type channelRelease interface {
	Close() error
	RestorationPossible() bool
}

func releaseConnection(connection protocolRelease, channel channelRelease) error {
	if !channel.RestorationPossible() {
		return errors.Join(errors.New("protocol restoration unavailable after terminal adapter loss"), channel.Close())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := connection.Release(ctx)
	cancel()
	if err != nil && connection.CleanupPending() && channel.RestorationPossible() {
		return err
	}
	return errors.Join(err, channel.Close())
}
