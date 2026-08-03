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
	accessPort       = dap.APSel(0)
	cpuidAddress     = uint32(0xe000ed00)
)

type identity struct {
	dpidr dap.DPIDRInfo
	apidr uint32
	cpuid uint32
}

type savedAPState struct {
	csw       uint32
	tar       uint32
	saved     bool
	connected bool
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()

	info, err := readIdentity(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := printIdentity(os.Stdout, info); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readIdentity(ctx context.Context) (_ identity, err error) {
	ch, err := openChannel(ctx)
	if err != nil {
		return identity{}, err
	}
	conn := swd.New(ch)
	if err := conn.JTAGToSWD(ctx); err != nil {
		return identity{}, errors.Join(err, ch.Close())
	}
	dp := dap.NewSWDP(conn)
	state := &savedAPState{}
	defer func() {
		err = errors.Join(err, restoreState(dp, state), ch.Close())
	}()

	dpidr, err := dp.Connect(ctx)
	if err != nil {
		return identity{}, err
	}
	state.connected = true
	apidr, err := dp.ReadAP(ctx, accessPort, dap.APIDR)
	if err != nil {
		return identity{}, err
	}
	mem, err := dap.NewMemAP(ctx, dp, accessPort)
	if err != nil {
		return identity{}, err
	}
	state.csw, err = dp.ReadAP(ctx, accessPort, dap.APCSW)
	if err != nil {
		return identity{}, err
	}
	state.tar, err = dp.ReadAP(ctx, accessPort, dap.APTAR)
	if err != nil {
		return identity{}, err
	}
	state.saved = true
	cpuid, err := mem.ReadWord(ctx, cpuidAddress)
	if err != nil {
		return identity{}, err
	}
	if cpuid>>24 != 0x41 || cpuid>>4&0x0fff == 0 {
		return identity{}, fmt.Errorf(
			"ostiole: CPUID %#08x is not a plausible Cortex-M identity",
			cpuid,
		)
	}
	return identity{dpidr: dpidr, apidr: apidr, cpuid: cpuid}, nil
}

func openChannel(ctx context.Context) (*ftdi.Channel, error) {
	enum := usb.New()
	devs, err := enum.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		return nil, err
	}
	if len(devs) != 1 {
		return nil, fmt.Errorf(
			"ostiole: require exactly one supported FTDI attachment; found %d",
			len(devs),
		)
	}
	dev, err := enum.Open(ctx, devs[0])
	if err != nil {
		return nil, err
	}
	return ftdi.Open(ctx, dev, ftdi.Config{
		ClockHz:   400_000,
		ProductID: devs[0].PID,
		Port:      ftdi.PortA,
		Interface: ftdi.SWD,
	})
}

func restoreState(dp *dap.DebugPort, state *savedAPState) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	var err error
	if state.saved {
		err = errors.Join(
			err,
			dp.WriteAP(ctx, accessPort, dap.APTAR, state.tar),
			dp.WriteAP(ctx, accessPort, dap.APCSW, state.csw),
		)
	}
	if state.connected {
		err = errors.Join(err, dp.WriteDP(ctx, dap.SELECT, 0))
	}
	return errors.Join(err, dp.Release(ctx))
}

func printIdentity(w io.Writer, info identity) error {
	_, err := fmt.Fprintf(
		w,
		"DPIDR=%#08x AP0_IDR=%#08x CPUID=%#08x\n",
		info.dpidr.Raw,
		info.apidr,
		info.cpuid,
	)
	return err
}
