// Package discovery registers host USB enumeration with discover.
// Importing it registers only; discovery performs the first hardware access.
package discovery

import (
	"context"
	"fmt"

	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/usb"
)

// ID identifies host USB enumeration.
const ID discover.ProviderID = "usb"

// Attachment is one detached USB identity. It owns no host resource.
type Attachment struct{ identity usb.DeviceInfo }

// NewAttachment copies an enumerated identity without opening it.
func NewAttachment(identity usb.DeviceInfo) Attachment { return Attachment{identity} }

// Identity returns the exact snapshot needed by usb.Enumerator.Open.
func (a Attachment) Identity() usb.DeviceInfo { return a.identity }

// Info returns common metadata. The fixed-width key sorts bus, address, VID,
// and PID numerically. Missing or duplicate serials cannot guarantee physical
// ordering across replugging because the host may assign another address.
func (a Attachment) Info() discover.AttachmentInfo {
	i := a.identity
	return discover.AttachmentInfo{
		Product: i.Product, Serial: i.Serial, Location: fmt.Sprintf("%d:%d", i.Bus, i.Address),
		Key: fmt.Sprintf("%03d:%03d:%04x:%04x", i.Bus, i.Address, i.VID, i.PID),
	}
}

var provider = discover.NewTransportProvider(ID, enumerate)

func enumerate(ctx context.Context) ([]discover.Attachment, error) {
	devices, err := usb.New().List(ctx, []usb.DeviceFilter{usb.AllDevices()})
	attachments := make([]discover.Attachment, len(devices))
	for i, device := range devices {
		attachments[i] = NewAttachment(device)
	}
	return attachments, err
}

// Register installs USB enumeration. A nil registry selects the package
// registry. Direct duplicate registration is an error.
func Register(registry *discover.Registry) error {
	if registry == nil {
		return discover.RegisterTransport(provider)
	}
	return registry.RegisterTransport(provider)
}

// Ensure installs the shared USB dependency, accepting this same provider if
// already installed. A nil registry selects the package registry.
func Ensure(registry *discover.Registry) error {
	if registry == nil {
		return discover.EnsureTransport(provider)
	}
	return registry.EnsureTransport(provider)
}

func init() {
	if err := Register(nil); err != nil {
		panic(err)
	}
}
