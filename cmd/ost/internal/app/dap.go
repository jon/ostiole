package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/jon/ostiole/dap"
)

type apIdentity struct {
	dpidr     dap.DPIDRInfo
	selection dap.APSel
	idr       uint32
}

func runDAP(ctx context.Context, args []string, stdout io.Writer, ops operations) error {
	if len(args) == 2 && args[0] == "dp" && args[1] == "id" {
		return runDP(ctx, stdout, ops)
	}
	if len(args) >= 2 && args[0] == "ap" && args[1] == "id" {
		return runAP(ctx, args[2:], stdout, ops)
	}
	return &usageError{message: "dap requires a supported id command"}
}

func runDP(ctx context.Context, stdout io.Writer, ops operations) error {
	info, err := ops.inspectDP(ctx)
	if err != nil {
		return err
	}
	const format = "DPIDR=%#08x REVISION=%d PART=%#02x " +
		"MINIMAL=%t VERSION=%d DESIGNER=%#03x\n"
	_, err = fmt.Fprintf(stdout, format, info.Raw, info.Revision, info.Part,
		info.Minimal, info.Version, info.Designer)
	return err
}

func runAP(ctx context.Context, args []string, stdout io.Writer, ops operations) error {
	selection, err := parseAP(args)
	if err != nil {
		return err
	}
	info, err := ops.inspectAP(ctx, selection)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "DPIDR=%#08x AP%d_IDR=%#08x\n",
		info.dpidr.Raw, info.selection, info.idr)
	return err
}

func parseAP(args []string) (dap.APSel, error) {
	flags := flag.NewFlagSet("dap ap id", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	selection := flags.Uint("ap", 0, "access-port number")
	if err := flags.Parse(args); err != nil {
		return 0, &usageError{message: fmt.Sprintf("dap ap id: %v", err)}
	}
	if flags.NArg() != 0 {
		return 0, &usageError{message: "dap ap id accepts no arguments"}
	}
	if *selection > 255 {
		return 0, &usageError{message: "dap ap id: --ap exceeds 255"}
	}
	return dap.APSel(*selection), nil
}

func inspectDP(ctx context.Context) (_ dap.DPIDRInfo, err error) {
	session, err := openDAP(ctx)
	if err != nil {
		return dap.DPIDRInfo{}, err
	}
	defer func() {
		err = errors.Join(err, session.close())
	}()
	return session.identity, nil
}

func inspectAP(ctx context.Context, selection dap.APSel) (_ apIdentity, err error) {
	session, err := openDAP(ctx)
	if err != nil {
		return apIdentity{}, err
	}
	defer func() {
		err = errors.Join(err, session.close())
	}()
	idr, err := session.port.ReadAP(ctx, selection, dap.APIDR)
	if err != nil {
		return apIdentity{}, err
	}
	return apIdentity{
		dpidr:     session.identity,
		selection: selection,
		idr:       idr,
	}, nil
}
