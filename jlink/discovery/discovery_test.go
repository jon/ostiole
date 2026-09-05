package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/jlink"
	"github.com/jon/ostiole/usb"
	usbdiscovery "github.com/jon/ostiole/usb/discovery"
)

func TestReviewedIdentitiesOnly(t *testing.T) {
	var r discover.Registry
	if err := r.RegisterTransport(discover.NewTransportProvider(usbdiscovery.ID, func(context.Context) ([]discover.Attachment, error) {
		return []discover.Attachment{
			usbdiscovery.NewAttachment(usb.DeviceInfo{VID: jlink.VID, PID: 0x0101, Serial: "chosen", Address: 1}),
			usbdiscovery.NewAttachment(usb.DeviceInfo{VID: jlink.VID, PID: 0x0106, Address: 2}),
			usbdiscovery.NewAttachment(usb.DeviceInfo{VID: jlink.VID, PID: 0x1008, Address: 3}),
		}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(provider); err != nil {
		t.Fatal(err)
	}
	i, err := r.Probes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, err := i.Select(discover.Selection{})
	if err != nil || c.Info().Provider != ID || c.Info().Serial != "chosen" {
		t.Fatalf("selection: %v, %v", c.Info(), err)
	}
	var explicit discover.Registry
	if err := Register(&explicit); err != nil {
		t.Fatal(err)
	}
	if err := Register(&explicit); !errors.Is(err, discover.ErrDuplicateProvider) {
		t.Fatal(err)
	}
}
