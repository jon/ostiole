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
	"github.com/jon/ostiole/internal/command"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/target/cortexm"
	"github.com/jon/ostiole/usb"
)

const (
	operationTimeout = 5 * time.Second
	cleanupTimeout   = time.Second
)

var accessPort = dap.NewAPSel(0)

type identity struct {
	dpidr     dap.DPIDRInfo
	apidr     dap.APIDRInfo
	processor cortexm.Identity
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	signalCtx, stop := command.SignalContext()
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, operationTimeout)
	defer cancel()

	info, err := readIdentity(ctx)
	if err != nil {
		return err
	}
	return printIdentity(os.Stdout, info)
}

func readIdentity(ctx context.Context) (_ identity, err error) {
	ch, err := openChannel(ctx)
	if err != nil {
		return identity{}, err
	}
	conn := swd.New(ch)
	dp := dap.NewDebugPort(dap.SWDP(conn))
	var mem *dap.MemAP
	defer func() { err = errors.Join(err, releaseIdentity(mem, dp, ch)) }()

	portID, err := dp.Connect(ctx)
	if err != nil {
		return identity{}, err
	}
	apidr, err := dp.ReadAPIDR(ctx, accessPort)
	if err != nil {
		return identity{}, err
	}
	mem, err = dap.OpenMemAP(ctx, dp, accessPort)
	if err != nil {
		return identity{}, err
	}
	processor, err := cortexm.Identify(ctx, mem)
	if err != nil {
		return identity{}, err
	}
	dpidr, _ := portID.DPIDR()
	return identity{dpidr: dpidr, apidr: apidr, processor: processor}, nil
}

func openChannel(ctx context.Context) (*ftdi.Channel, error) {
	enum := usb.New()
	devs, err := enum.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		return nil, err
	}
	if len(devs) != 1 {
		return nil, fmt.Errorf("ostiole: require exactly one supported FTDI attachment; found %d", len(devs))
	}
	dev, err := enum.Open(ctx, devs[0])
	if err != nil {
		return nil, err
	}
	ch, err := ftdi.Open(ctx, dev, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 1_000_000})
	if err != nil {
		closeOwner := dev.Close
		if ch != nil {
			closeOwner = ch.Close
		}
		return nil, errors.Join(err, closeOwner())
	}
	return ch, nil
}

func printIdentity(w io.Writer, info identity) error {
	_, err := fmt.Fprintf(w, "DPIDR=%#08x AP0_IDR=%#08x CPUID=%#08x\n",
		info.dpidr.Raw, info.apidr.Raw, info.processor.Raw)
	return err
}

type protocolRelease interface {
	Release(context.Context) error
}

type channelRelease interface {
	Close() error
	RestorationPossible() bool
}

type debugPortRelease interface {
	protocolRelease
	CleanupPending() bool
}

func releaseIdentity(memory protocolRelease, port debugPortRelease, channel channelRelease) error {
	for i, owner := range []protocolRelease{memory, port} {
		if !channel.RestorationPossible() {
			return errors.Join(errors.New("protocol restoration unavailable after terminal adapter loss"), channel.Close())
		}
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		err := owner.Release(ctx)
		cancel()
		if err != nil {
			if channel.RestorationPossible() && (i == 0 || port.CleanupPending()) {
				return err
			}
			return errors.Join(err, channel.Close())
		}
	}
	return channel.Close()
}
