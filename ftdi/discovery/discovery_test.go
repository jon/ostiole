package discovery

import (
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/usb"
	usbdiscovery "github.com/jon/ostiole/usb/discovery"
)

func TestFTDIPortCandidates(t *testing.T) {
	var r discover.Registry
	if err := r.RegisterTransport(discover.NewTransportProvider(usbdiscovery.ID, func(context.Context) ([]discover.Attachment, error) {
		return []discover.Attachment{
			usbdiscovery.NewAttachment(usb.DeviceInfo{VID: ftdi.VID, PID: ftdi.PIDFT2232H, Serial: "dual", Address: 2}),
			usbdiscovery.NewAttachment(usb.DeviceInfo{VID: ftdi.VID, PID: ftdi.PIDFT232H, Serial: "single", Address: 1}),
			usbdiscovery.NewAttachment(usb.DeviceInfo{VID: 0xffff, PID: ftdi.PIDFT2232H, Address: 3}),
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
	got := slices.Collect(iter.Seq[discover.Candidate](i))
	if len(got) != 3 {
		t.Fatalf("candidates: %d", len(got))
	}
	for n, function := range []string{"A", "B", "A"} {
		if got[n].Info().Function != function || got[n].Info().Provider != ID {
			t.Fatalf("candidate %d: %v", n, got[n].Info())
		}
	}
	if _, err := i.Select(discover.Selection{Serial: "dual"}); !errors.Is(err, discover.ErrCandidateAmbiguous) {
		t.Fatal(err)
	}
}

func TestRegisterInstallsUSB(t *testing.T) {
	var r discover.Registry
	if err := Register(&r); err != nil {
		t.Fatal(err)
	}
	if err := usbdiscovery.Ensure(&r); err != nil {
		t.Fatal(err)
	}
	if err := Register(&r); !errors.Is(err, discover.ErrDuplicateProvider) {
		t.Fatal(err)
	}
}
