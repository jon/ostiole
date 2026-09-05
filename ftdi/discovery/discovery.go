// Package discovery registers FTDI MPSSE probe bindings and USB discovery.
package discovery

import (
	"context"
	"fmt"

	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
	usbdiscovery "github.com/jon/ostiole/usb/discovery"
)

// ID identifies FTDI MPSSE probe bindings.
const ID discover.ProviderID = "ftdi"

var provider = discover.NewProbeProvider(ID, usbdiscovery.ID, classify)

// Register installs FTDI classification and its shared USB dependency.
// A nil registry selects the package registry. Duplicate bindings are errors.
func Register(registry *discover.Registry) error {
	if err := usbdiscovery.Ensure(registry); err != nil {
		return err
	}
	if registry == nil {
		return discover.RegisterProbe(provider)
	}
	return registry.RegisterProbe(provider)
}

func classify(_ context.Context, transport discover.Transport) ([]discover.Candidate, error) {
	a, ok := transport.Attachment().(usbdiscovery.Attachment)
	if !ok {
		return nil, fmt.Errorf("ftdi: unexpected USB attachment %T", transport.Attachment())
	}
	var candidates []discover.Candidate
	for _, binding := range ftdi.Candidates([]usb.DeviceInfo{a.Identity()}) {
		function := "A"
		if binding.Port == ftdi.PortB {
			function = "B"
		}
		info := a.Info()
		metadata := probe.Info{Product: info.Product, Serial: info.Serial, Location: info.Location, Function: function}
		candidates = append(candidates, discover.NewCandidate(metadata, info.Key+":"+function, func(ctx context.Context) (*probe.Probe, error) {
			return ftdi.OpenProbe(ctx, binding.Device, binding.Port)
		}))
	}
	return candidates, nil
}

func init() {
	if err := Register(nil); err != nil {
		panic(err)
	}
}
