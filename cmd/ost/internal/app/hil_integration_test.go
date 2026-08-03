//go:build integration

package app

import (
	"os"
	"testing"

	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/usb"
)

func requireHILBench(t *testing.T) {
	t.Helper()
	if os.Getenv("OSTIOLE_FTDI_HIL") != "1" {
		t.Skip("OSTIOLE_FTDI_HIL is not 1")
	}
	devices, err := usb.New().List(t.Context(), ftdi.SupportedDevices())
	if err != nil {
		t.Fatalf("enumerate supported FTDI attachments: %v", err)
	}
	if len(devices) != 1 {
		t.Skipf("require exactly one supported FTDI attachment; found %d", len(devices))
	}
}
