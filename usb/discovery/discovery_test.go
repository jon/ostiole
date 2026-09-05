package discovery

import (
	"errors"
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
