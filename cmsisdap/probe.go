package cmsisdap

import (
	"context"

	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

// OpenProbe opens one exact USB attachment without claiming a command interface.
// The returned owner does not advertise a wire backend: DAP_SWD_Sequence cannot
// preserve the direction boundaries required by a general SWD wire.
func OpenProbe(ctx context.Context, identity usb.DeviceInfo) (*probe.Probe, error) {
	return probeusb.OpenAttachment(ctx, identity)
}
