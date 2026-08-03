package app

import (
	"context"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/usb"
)

type operations struct {
	listFTDI  func(context.Context) ([]usb.DeviceInfo, error)
	readDPIDR func(context.Context) (uint32, error)
	inspectDP func(context.Context) (dap.DPIDRInfo, error)
	inspectAP func(context.Context, dap.APSel) (apIdentity, error)
}

func systemOperations() operations {
	return operations{
		listFTDI:  listFTDI,
		readDPIDR: readDPIDR,
		inspectDP: inspectDP,
		inspectAP: inspectAP,
	}
}
