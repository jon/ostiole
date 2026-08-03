package app

import (
	"context"

	"github.com/jon/ostiole/usb"
)

type operations struct {
	listFTDI  func(context.Context) ([]usb.DeviceInfo, error)
	readDPIDR func(context.Context) (uint32, error)
}

func systemOperations() operations {
	return operations{
		listFTDI:  listFTDI,
		readDPIDR: readDPIDR,
	}
}
