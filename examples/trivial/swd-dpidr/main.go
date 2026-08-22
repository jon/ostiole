package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/jon/ostiole/ftdi"
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
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
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
	ch, err := ftdi.Open(ctx, dev, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 400_000})
	if err != nil {
		return 0, errors.Join(err, dev.Close())
	}
	defer func() {
		err = errors.Join(err, ch.Close())
	}()
	conn := swd.New(ch)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = errors.Join(err, conn.Release(cleanupCtx))
	}()
	value, err = conn.Connect(ctx)
	if err != nil {
		return 0, err
	}
	if value == 0 || value == math.MaxUint32 || value&1 == 0 {
		return 0, fmt.Errorf("ostiole: invalid DPIDR %#08x", value)
	}
	return value, nil
}
