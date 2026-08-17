package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

const (
	operationTimeout = 5 * time.Second
	cleanupTimeout   = time.Second
)

var accessPort = dap.NewAPSel(0)

type identity struct {
	dpidr dap.DPIDRInfo
	apidr dap.APIDRInfo
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()

	info, err := readIdentity(ctx)
	if err != nil {
		return err
	}
	return printIdentity(os.Stdout, info)
}

func readIdentity(ctx context.Context) (_ identity, err error) {
	enum := usb.New()
	devs, err := enum.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		return identity{}, err
	}
	if len(devs) != 1 {
		return identity{}, fmt.Errorf("ostiole: require exactly one supported FTDI attachment; found %d", len(devs))
	}
	dev, err := enum.Open(ctx, devs[0])
	if err != nil {
		return identity{}, err
	}
	ch, err := ftdi.Open(ctx, dev, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 400_000})
	if err != nil {
		return identity{}, errors.Join(err, dev.Close())
	}
	conn := swd.New(ch)
	if err := conn.JTAGToSWD(ctx); err != nil {
		return identity{}, errors.Join(err, ch.Close())
	}
	dp := dap.NewSWDP(conn)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		err = errors.Join(err, dp.Release(cleanupCtx), ch.Close())
	}()

	dpidr, err := dp.Connect(ctx)
	if err != nil {
		return identity{}, err
	}
	apidr, err := dp.ReadAPIDR(ctx, accessPort)
	if err != nil {
		return identity{}, err
	}
	if apidr.Raw == 0 {
		return identity{}, errors.New("ostiole: AP0 is absent")
	}
	return identity{dpidr: dpidr, apidr: apidr}, nil
}

func printIdentity(w io.Writer, info identity) error {
	_, err := fmt.Fprintf(w, "DPIDR=%#08x AP0_IDR=%#08x\n", info.dpidr.Raw, info.apidr.Raw)
	return err
}
