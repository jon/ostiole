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
	if got[0].Info().Binding == got[1].Info().Binding {
		t.Fatal("two FTDI functions share a binding")
	}
	selected, err := i.Select(discover.Selection{Binding: got[1].Info().Binding})
	if err != nil || selected.Info().Function != "B" || selected.Info().Binding != got[1].Info().Binding {
		t.Fatalf("exact port-B binding selection: %v, %v", selected.Info(), err)
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

func TestSeriallessFTDIFunctionSelection(t *testing.T) {
	var r discover.Registry
	if err := r.RegisterTransport(discover.NewTransportProvider(usbdiscovery.ID, func(context.Context) ([]discover.Attachment, error) {
		return []discover.Attachment{usbdiscovery.NewAttachment(usb.DeviceInfo{VID: ftdi.VID, PID: ftdi.PIDFT4232H, Bus: 3, Address: 7})}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(provider); err != nil {
		t.Fatal(err)
	}
	inventory, err := r.Probes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, function := range []string{"A", "B"} {
		selected, err := inventory.Select(discover.Selection{Provider: ID, Location: "3:7", Function: function})
		if err != nil || selected.Info().Function != function || selected.Info().Serial != "" {
			t.Fatalf("serial-less function%s: %v, %v", function, selected.Info(), err)
		}
		bound, err := inventory.Select(discover.Selection{Binding: selected.Info().Binding})
		if err != nil || bound.Info().Function != function {
			t.Fatalf("serial-less binding%s: %v, %v", function, bound.Info(), err)
		}
	}
}
