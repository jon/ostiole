//go:build integration

package cortexm_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/target/cortexm"
	"github.com/jon/ostiole/usb"
)

func TestIdentifyCortexMOverFTDI(t *testing.T) {
	if os.Getenv("OSTIOLE_FTDI_HIL") != "1" {
		t.Skip("OSTIOLE_FTDI_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	enumerator := usb.New()
	devices, err := enumerator.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Skipf("require exactly one supported FTDI attachment; found %d", len(devices))
	}
	device, err := enumerator.Open(ctx, devices[0])
	if err != nil {
		t.Fatal(err)
	}
	channel, err := ftdi.Open(ctx, device, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(errors.Join(err, device.Close()))
	}
	connection := swd.New(channel)
	if err := connection.JTAGToSWD(ctx); err != nil {
		if closeErr := channel.Close(); closeErr != nil {
			t.Errorf("close FTDI channel: %v", closeErr)
		}
		t.Fatal(err)
	}
	debugPort := dap.NewSWDP(connection)
	var memory *dap.MemAP
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if err := memory.Release(cleanupCtx); err != nil {
			t.Errorf("release MEM-AP: %v", err)
		}
		if err := debugPort.Release(cleanupCtx); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
		if err := channel.Close(); err != nil {
			t.Errorf("close FTDI channel: %v", err)
		}
	})

	if _, err := debugPort.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	memory, err = dap.NewMemAP(ctx, debugPort, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := cortexm.Identify(ctx, memory)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("CPUID=%#08x part=%#03x", info.Raw, info.Part)
}
