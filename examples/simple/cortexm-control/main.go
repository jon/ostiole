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

func run() error {
	provider := flag.String("provider", "", "required probe provider")
	serial := flag.String("serial", "", "required probe serial")
	ap := flag.Int("ap", -1, "required MEM-AP index (0..255)")
	allow := flag.Bool("allow-control", false, "allow enabling debug, halting, stepping, and resuming the processor")
	step := flag.Bool("step", false, "perform one architectural step while halted")
	clock := flag.Uint64("clock", 1_000_000, "maximum SWD clock in Hz")
	flag.Parse()
	if *clock < 1000 || *clock > 1<<32-1 {
		return errors.New("require -clock 1000..4294967295 Hz")
	}
	if !*allow || *provider == "" || *serial == "" || *ap < 0 || *ap > 255 || flag.NArg() != 0 {
		return errors.New("require -allow-control, -provider, -serial, and -ap 0..255")
	}
	selection := discover.Selection{Provider: discover.ProviderID(*provider), Serial: *serial}
	return runControl(selection, dap.NewAPSel(uint8(*ap)), uint32(*clock), *step)
}

func runControl(selection discover.Selection, ap dap.APSel, clock uint32, step bool) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := armdebug.Open(ctx, selection, armdebug.Config{
		Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: clock}),
	})
	var core *cortexm.Target
	if c != nil {
		defer func() { err = errors.Join(err, release(core, c)) }()
	}
	if err != nil {
		return err
	}
	memory, err := c.OpenMemAP(ctx, ap)
	if err != nil {
		return err
	}
	core, err = cortexm.Acquire(ctx, memory)
	if err != nil {
		return err
	}
	return control(ctx, core, step)
}

type controlledTarget interface {
	Identity() cortexm.Identity
	Halted(context.Context) (bool, error)
	Halt(context.Context) error
	ReadRegister(context.Context, cortexm.Register) (uint32, error)
	Step(context.Context) error
	Resume(context.Context) error
}

func control(ctx context.Context, core controlledTarget, step bool) error {
	inherited, err := core.Halted(ctx)
	if err != nil {
		return err
	}
	if err := core.Halt(ctx); err != nil {
		return err
	}
	if inherited {
		fmt.Printf("CPUID=%#08x already halted; preserving halt\n", core.Identity().Raw)
	} else {
		fmt.Printf("CPUID=%#08x halted\n", core.Identity().Raw)
	}
	for _, reg := range []struct {
		name string
		id   cortexm.Register
	}{{"PC", cortexm.PC}, {"SP", cortexm.SP}, {"R0", cortexm.R0}, {"R4", cortexm.R4}} {
		value, err := core.ReadRegister(ctx, reg.id)
		if err != nil {
			return err
		}
		fmt.Printf("%s=%#08x\n", reg.name, value)
	}
	if inherited {
		return nil
	}
	if step {
		if err := core.Step(ctx); err != nil {
			return err
		}
		pc, err := core.ReadRegister(ctx, cortexm.PC)
		if err != nil {
			return err
		}
		fmt.Printf("stepped; PC=%#08x\n", pc)
	}
	if err := core.Resume(ctx); err != nil {
		return err
	}
	fmt.Println("resumed")
	return nil
}

type targetRelease interface {
	Release(context.Context) error
}

type connectionRelease interface {
	Close() error
	RestorationPossible() bool
}

func release(core targetRelease, c connectionRelease) error {
	var attempts, err error
	if c.RestorationPossible() {
		for range 3 {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = core.Release(ctx)
			cancel()
			attempts = errors.Join(attempts, err)
			if err == nil || !c.RestorationPossible() {
				break
			}
		}
		if err != nil && c.RestorationPossible() {
			return fmt.Errorf("target restoration remains pending; memory owner retained: %w", attempts)
		}
	}
	for range 3 {
		err = c.Close()
		attempts = errors.Join(attempts, err)
		if err == nil {
			return attempts
		}
	}
	return fmt.Errorf("connection cleanup reported errors after three attempts: %w", attempts)
}
