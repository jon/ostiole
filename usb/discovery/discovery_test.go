package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/usb"
)

func TestAttachmentIsDetached(t *testing.T) {
	identity := usb.DeviceInfo{VID: 0x1234, PID: 0x5678, Bus: 3, Address: 12, Product: "probe", Serial: "serial"}
	a := NewAttachment(identity)
	identity.Serial = "changed"
	if a.Identity().Serial != "serial" || a.Info().Location != "3:12" || a.Info().Key != "003:012:1234:5678" {
		t.Fatalf("attachment: %v", a.Info())
	}
}

func TestRegistrationAndDependencyInstallation(t *testing.T) {
	var r discover.Registry
	if err := Register(&r); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(&r); err != nil {
		t.Fatal(err)
	}
	if err := Register(&r); !errors.Is(err, discover.ErrDuplicateProvider) {
		t.Fatal(err)
	}
}

func TestEqualSerialUSBLocationsSortLexicographically(t *testing.T) {
	attachments := []discover.Attachment{
		NewAttachment(usb.DeviceInfo{Bus: 2, Address: 2, Serial: "same"}),
		NewAttachment(usb.DeviceInfo{Bus: 10, Address: 2, Serial: "same"}),
		NewAttachment(usb.DeviceInfo{Bus: 2, Address: 10, Serial: "same"}),
	}
	provider := discover.NewTransportProvider("fixture", func(context.Context) ([]discover.Attachment, error) {
		return attachments, nil
	})
	var registry discover.Registry
	if err := registry.RegisterTransport(provider); err != nil {
		t.Fatal(err)
	}
	inventory, err := registry.Transports(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var locations []string
	for transport := range inventory.All() {
		locations = append(locations, transport.Info().Location)
	}
	if !reflect.DeepEqual(locations, []string{"10:2", "2:10", "2:2"}) {
		t.Fatalf("USB location ordering: %v", locations)
	}
}
