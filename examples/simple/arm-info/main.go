package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/discover"
	_ "github.com/jon/ostiole/discover/probes"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/target/cortexm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	provider := flag.String("provider", "", "exact probe provider")
	serial := flag.String("serial", "", "exact probe serial")
	function := flag.String("function", "", "exact probe function")
	ap := flag.Int("ap", -1, "required MEM-AP index (0..255)")
	clock := flag.Uint64("clock", 1_000_000, "maximum SWD clock in Hz")
	flag.Parse()
	if *clock < 1000 || *clock > 1<<32-1 {
		return errors.New("require -clock 1000..4294967295 Hz")
	}
	if *ap < 0 || *ap > 255 || flag.NArg() != 0 {
		return errors.New("require -ap 0..255 and no positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := armdebug.Open(ctx, discover.Selection{
		Provider: discover.ProviderID(*provider), Serial: *serial, Function: *function,
	}, armdebug.Config{Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: uint32(*clock)})})
	if c != nil {
		defer func() { err = errors.Join(err, closeConnection(c)) }()
	}
	if err != nil {
		return err
	}
	dpidr, err := c.Port().ReadDP(ctx, dap.DPIDR)
	if err != nil {
		return err
	}
	selection := dap.NewAPSel(uint8(*ap))
	apidr, err := c.Port().ReadAPIDR(ctx, selection)
	if err != nil {
		return err
	}
	memory, err := c.OpenMemAP(ctx, selection)
	if err != nil {
		return err
	}
	processor, err := cortexm.Identify(ctx, memory)
	if err != nil {
		return err
	}
	fmt.Printf("probe=%+v DPIDR=%#08x AP%d=%#08x CPUID=%#08x\n", c.Info(), dpidr, *ap, apidr.Raw, processor.Raw)
	return nil
}

func closeConnection(c *armdebug.Conn) error {
	var err error
	for range 3 {
		if err = c.Close(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("cleanup remains pending after three attempts: %w", err)
}
