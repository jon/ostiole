package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jon/ostiole/dap"
)

func runDAP(ctx context.Context, args []string, stdout io.Writer, ops operations) error {
	if len(args) != 2 || args[0] != "dp" || args[1] != "id" {
		return &usageError{message: "dap requires the dp id command"}
	}
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
