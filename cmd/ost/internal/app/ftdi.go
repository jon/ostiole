package app

import (
	"context"
	"fmt"
	"io"

	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/usb"
)

type operations struct {
	listFTDI func(context.Context) ([]usb.DeviceInfo, error)
}

func systemOperations() operations {
	return operations{listFTDI: listFTDI}
}

func listFTDI(ctx context.Context) ([]usb.DeviceInfo, error) {
	return usb.New().List(ctx, ftdi.SupportedDevices())
}

func runFTDI(ctx context.Context, args []string, stdout io.Writer, ops operations) error {
	if len(args) != 1 || args[0] != "list" {
		return &usageError{message: "ftdi requires the list command"}
	}
	devices, err := ops.listFTDI(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		_, err := fmt.Fprintf(stdout, "%03d:%03d %04x:%04x\n",
			device.Bus, device.Address, device.VID, device.PID)
		if err != nil {
			return err
		}
	}
	return nil
}
