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
	ap := flag.Int("ap", -1, "required MEM-AP index (0..255)")
	address := flag.String("base", "", "override the MEM-AP debug base with a known identification page")
	flag.Parse()
	base, err := parseBase(*address)
	if err != nil {
		return err
	}
	if *ap < 0 || *ap > 255 || flag.NArg() != 0 {
		return errors.New("require -ap 0..255 and no positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := armdebug.Open(ctx, discover.Selection{
		Provider: discover.ProviderID(*provider), Serial: *serial, Function: *function,
	}, armdebug.Config{Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 100_000})})
	if c != nil {
		defer func() { err = errors.Join(err, closeConnection(c)) }()
	}
	if err != nil {
		return err
	}
	memory, err := c.OpenMemAP(ctx, dap.NewAPSel(uint8(*ap)))
	if err != nil {
		return err
	}
	if *address == "" {
		var present bool
		base, present, err = memory.ReadDebugBase(ctx)
		if err != nil {
			return err
		}
		if !present {
			return errors.New("selected MEM-AP advertises no debug entry")
		}
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
