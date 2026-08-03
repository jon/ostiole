package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/target/cortexm"
)

type targetIdentity struct {
	dpidr     dap.DPIDRInfo
	selection dap.APSel
	apidr     uint32
	processor cortexm.Identity
}

func runTarget(ctx context.Context, args []string, stdout io.Writer, ops operations) error {
	if len(args) < 2 || args[0] != "cortex-m" || args[1] != "id" {
		return &usageError{message: "target requires the cortex-m id command"}
	}
	selection, err := parseAP("target cortex-m id", args[2:])
	if err != nil {
		return err
	}
	info, err := ops.identifyCortexM(ctx, selection)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "DPIDR=%#08x AP%d_IDR=%#08x CPUID=%#08x\n",
		info.dpidr.Raw, info.selection, info.apidr, info.processor.Raw)
	return err
}

func identifyCortexM(ctx context.Context, selection dap.APSel) (_ targetIdentity, err error) {
	session, err := openDAP(ctx)
	if err != nil {
		return targetIdentity{}, err
	}
	defer func() {
		err = errors.Join(err, session.close())
	}()
	apidr, err := session.port.ReadAP(ctx, selection, dap.APIDR)
	if err != nil {
		return targetIdentity{}, err
	}
	session.memory, err = dap.NewMemAP(ctx, session.port, selection)
	if err != nil {
		return targetIdentity{}, err
	}
	processor, err := cortexm.Identify(ctx, session.memory)
	if err != nil {
		return targetIdentity{}, err
	}
	return targetIdentity{
		dpidr:     session.identity,
		selection: selection,
		apidr:     apidr,
		processor: processor,
	}, nil
}
