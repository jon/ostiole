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
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()
	dpidr, err := readDPIDR(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("DPIDR=%#08x\n", dpidr)
}

func readDPIDR(ctx context.Context) (value uint32, err error) {
	enum := usb.New()
	devs, err := enum.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		return 0, err
	}
	if len(devs) != 1 {
		return 0, fmt.Errorf("ostiole: require exactly one supported FTDI attachment; found %d", len(devs))
	}
	dev, err := enum.Open(ctx, devs[0])
	if err != nil {
		return 0, err
	}
	ch, err := ftdi.Open(ctx, dev, ftdi.Config{
		ClockHz:   400_000,
		ProductID: devs[0].PID,
		Port:      ftdi.PortA,
		Interface: ftdi.SWD,
	})
	if err != nil {
		return 0, err
	}
	defer func() {
		err = errors.Join(err, ch.Close())
	}()
	conn := swd.New(ch)
	if err := conn.JTAGToSWD(ctx); err != nil {
		return 0, err
	}
	value, err = conn.Transfer(ctx, swd.Request{Read: true}, 0)
	if err != nil {
		return 0, err
	}
	if value == 0 || value == math.MaxUint32 || value&1 == 0 {
		return 0, fmt.Errorf("ostiole: invalid DPIDR %#08x", value)
	}
	return value, nil
}
