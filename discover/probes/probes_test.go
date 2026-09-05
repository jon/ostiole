package probes_test

import (
	"errors"
	"testing"

	cmsisdiscovery "github.com/jon/ostiole/cmsisdap/discovery"
	"github.com/jon/ostiole/discover"
	_ "github.com/jon/ostiole/discover/probes"
	ftdidiscovery "github.com/jon/ostiole/ftdi/discovery"
	jlinkdiscovery "github.com/jon/ostiole/jlink/discovery"
	usbdiscovery "github.com/jon/ostiole/usb/discovery"
)

func TestBundledAndExplicitRegistrations(t *testing.T) {
	var r discover.Registry
	for _, register := range []func(*discover.Registry) error{
		cmsisdiscovery.Register, ftdidiscovery.Register, jlinkdiscovery.Register,
	} {
		if err := register(nil); !errors.Is(err, discover.ErrDuplicateProvider) {
			t.Fatal(err)
		}
		if err := register(&r); err != nil {
			t.Fatal(err)
		}
	}
	if err := usbdiscovery.Ensure(&r); err != nil {
		t.Fatal(err)
	}
}
