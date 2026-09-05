// Package discovery registers CMSIS-DAP product-marker bindings and USB discovery.
package discovery

import (
	"context"
	"fmt"

	"github.com/jon/ostiole/cmsisdap"
	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
	usbdiscovery "github.com/jon/ostiole/usb/discovery"
)

// ID identifies CMSIS-DAP probe bindings.
const ID discover.ProviderID = "cmsisdap"

var provider = discover.NewProbeProvider(ID, usbdiscovery.ID, classify)

// Register installs CMSIS-DAP classification and its shared USB dependency.
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
		return nil, fmt.Errorf("cmsisdap: unexpected USB attachment %T", transport.Attachment())
	}
	if len(cmsisdap.Candidates([]usb.DeviceInfo{a.Identity()})) == 0 {
		return nil, nil
	}
	info := a.Info()
	metadata := probe.Info{Product: info.Product, Serial: info.Serial, Location: info.Location}
	return []discover.Candidate{discover.NewCandidate(metadata, info.Key, func(ctx context.Context) (*probe.Probe, error) {
		return cmsisdap.OpenProbe(ctx, a.Identity())
	})}, nil
}

func init() {
	if err := Register(nil); err != nil {
		panic(err)
	}
}
