package app

import (
	"context"
	"errors"
	"fmt"
	"io"
)

func runSWD(ctx context.Context, args []string, stdout io.Writer, ops operations) error {
	if len(args) != 1 || args[0] != "dpidr" {
		return &usageError{message: "swd requires the dpidr command"}
	}
	value, err := ops.readDPIDR(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "DPIDR=%#08x\n", value)
	return err
}

func readDPIDR(ctx context.Context) (_ uint32, err error) {
	session, err := openSWD(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		err = errors.Join(err, session.close())
	}()
	return session.connection.ReadDP(ctx, 0x00)
}
