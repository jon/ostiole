package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/coresight"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/discover"
	_ "github.com/jon/ostiole/discover/probes"
	"github.com/jon/ostiole/probe"
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
	apBase := flag.String("ap-base", "", "ADIv6 MEM-AP base address")
	debugSpace := flag.Bool("debug-space", false, "inspect the ADIv6 DP debug address space")
	ap := flag.Int("ap", -1, "ADIv5 MEM-AP index (0..255)")
	address := flag.String("base", "", "override the MEM-AP debug base with a known identification page")
	walk := flag.Bool("walk", false, "walk ROM tables with depth 8, 256 visits, and 4096 entry reads")
	clock := flag.Uint64("clock", 1_000_000, "maximum SWD clock in Hz")
	flag.Parse()
	if *clock < 1000 || *clock > 1<<32-1 {
		return errors.New("require -clock 1000..4294967295 Hz")
	}
	base, err := parseBase(*address)
	if err != nil {
		return err
	}
	selection, err := selectAP(*ap, *apBase, *debugSpace)
	if err != nil {
		return err
	}
	if flag.NArg() != 0 {
		return errors.New("no positional arguments permitted")
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
	var memory inspectionReader
	if *debugSpace {
		memory = c.Port().DebugSpace()
	} else {
		memory, err = c.OpenMemAP(ctx, selection)
		if err != nil {
			return err
		}
	}
	return inspectRoot(ctx, memory, *address, base, *walk)
}

type inspectionReader interface {
	coresight.ScalarReader
	ReadDebugBase(context.Context) (uint64, bool, error)
}

func inspectRoot(ctx context.Context, memory inspectionReader, override string, base uint64, walk bool) error {
	if override == "" {
		value, present, err := memory.ReadDebugBase(ctx)
		if err != nil {
			return err
		}
		if !present {
			return errors.New("selected address space advertises no debug entry")
		}
		base = value
	}
	return inspect(ctx, memory, base, walk)
}

func selectAP(index int, base string, debugSpace bool) (dap.APSel, error) {
	choices := 0
	if index != -1 {
		choices++
	}
	if base != "" {
		choices++
	}
	if debugSpace {
		choices++
	}
	if choices != 1 {
		return dap.APSel{}, errors.New("select exactly one of -ap, -ap-base, or -debug-space")
	}
	if debugSpace {
		return dap.APSel{}, nil
	}
	if base != "" {
		value, err := parseBase(base)
		if err != nil {
			return dap.APSel{}, err
		}
		return dap.APAt(value)
	}
	if index < 0 || index > 255 {
		return dap.APSel{}, errors.New("-ap must be 0..255")
	}
	return dap.NewAPSel(uint8(index)), nil
}

func inspect(ctx context.Context, memory coresight.ScalarReader, base uint64, walk bool) error {
	if walk {
		return printWalk(ctx, memory, base)
	}
	component, err := coresight.Identify(ctx, memory, base)
	if err != nil {
		return err
	}
	printComponent(component)
	return nil
}

func printComponent(component coresight.Component) {
	designer, jedec := component.Designer()
	fmt.Printf("base=%#x CIDR=%#08x PIDR=%#016x class=%#x designer=%#x JEP106=%t part=%#03x revision=%d\n",
		component.Base, component.CIDR, component.PIDR, component.Class(), designer, jedec, component.Part(), component.Revision())
	if component.Class() == 9 {
		fmt.Printf("DEVARCH=%#08x DEVID=%#08x DEVTYPE=%#02x\n", component.DEVARCH, component.DEVID, component.DEVTYPE)
	}
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

func parseBase(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	base, err := strconv.ParseUint(value, 0, 64)
	if err != nil || base&0xfff != 0 {
		return 0, errors.New("-base must be a 4 KiB aligned address")
	}
	return base, nil
}

func printWalk(ctx context.Context, memory coresight.ScalarReader, base uint64) error {
	limits := coresight.WalkLimits{MaxDepth: 8, MaxComponents: 256, MaxEntries: 4096}
	visits, err := coresight.Walk(ctx, memory, base, limits)
	for i, visit := range visits {
		fmt.Printf("visit=%d parent=%d entry=%d\n", i, visit.Parent, visit.Index)
		if visit.Component != nil {
			printComponent(*visit.Component)
		}
		if visit.Err != nil {
			fmt.Printf("%v\n", visit.Err)
		}
	}
	fmt.Printf("visits=%d complete=%t\n", len(visits), err == nil)
	return err
}
