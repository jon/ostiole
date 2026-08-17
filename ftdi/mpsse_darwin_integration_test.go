//go:build darwin && integration

package ftdi_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/usb"
)

func TestHILDarwinFT232HMPSSEHandshake(t *testing.T) {
	if os.Getenv("OSTIOLE_FT232H_DARWIN_HIL") != "1" {
		t.Skip("OSTIOLE_FT232H_DARWIN_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	bus := usb.New()
	devices, err := bus.List(ctx, []usb.DeviceFilter{usb.ExactDevice(ftdi.VID, ftdi.PIDFT232H)})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Skipf("require exactly one FT232H attachment; found %d", len(devices))
	}
	selected := devices[0]
	device, err := bus.Open(ctx, selected)
	if err != nil {
		t.Fatalf("open selected FT232H: %v", err)
	}
	channel, err := ftdi.Open(ctx, device, ftdi.Config{
		ClockHz:   400_000,
		ProductID: ftdi.PIDFT232H,
		Port:      ftdi.PortA,
		Interface: ftdi.SWD,
	})
	if err != nil {
		t.Fatalf("prepare FTDI MPSSE port A: %v", err)
	}
	if err := channel.Close(); err != nil {
		t.Fatalf("close FTDI MPSSE port A: %v", err)
	}
}
