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
	provider := flag.String("provider", "", "required probe provider")
	serial := flag.String("serial", "", "required probe serial")
	ap := flag.Int("ap", -1, "required MEM-AP index (0..255)")
	allow := flag.Bool("allow-control", false, "allow enabling debug, halting, and resuming the processor")
	flag.Parse()
	if !*allow || *provider == "" || *serial == "" || *ap < 0 || *ap > 255 || flag.NArg() != 0 {
		return errors.New("require -allow-control, -provider, -serial, and -ap 0..255")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := armdebug.Open(ctx, discover.Selection{
		Provider: discover.ProviderID(*provider), Serial: *serial,
	}, armdebug.Config{Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 100_000})})
	var core *cortexm.Target
	if c != nil {
		defer func() { err = errors.Join(err, release(core, c)) }()
	}
	if err != nil {
		return err
	}
	memory, err := c.OpenMemAP(ctx, dap.NewAPSel(uint8(*ap)))
	if err != nil {
		return err
	}
	core, err = cortexm.Acquire(ctx, memory)
	if err != nil {
		return err
	}
	return control(ctx, core)
}

func control(ctx context.Context, core *cortexm.Target) error {
	if err := core.Halt(ctx); err != nil {
		return err
	}
	fmt.Printf("CPUID=%#08x halted\n", core.Identity().Raw)
	if err := core.Resume(ctx); err != nil {
		return err
	}
	fmt.Println("resumed")
	return nil
}

func release(core *cortexm.Target, c *armdebug.Conn) error {
	var err error
	for range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = core.Release(ctx)
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("target restoration remains pending; memory owner retained: %w", err)
	}
	for range 3 {
		if err = c.Close(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("connection cleanup remains pending: %w", err)
}
