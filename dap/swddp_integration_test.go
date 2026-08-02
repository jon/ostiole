//go:build integration

package dap_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

func TestReadDPIDROverFTDI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dp := openHardwareDebugPort(t, ctx)
	raw, err := dp.ReadDP(ctx, dap.DPIDR)
	if err != nil {
		t.Fatal(err)
	}
	if raw == ^uint32(0) {
		t.Fatalf("DPIDR = %#08x", raw)
	}
	info, err := dap.DecodeDPIDR(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"DPIDR=%#08x version=%d designer=%#03x",
		info.Raw,
		info.Version,
		info.Designer,
	)
}

func openHardwareDebugPort(t *testing.T, ctx context.Context) *dap.DebugPort {
	t.Helper()
	if os.Getenv("OSTIOLE_FTDI_HIL") != "1" {
		t.Skip("OSTIOLE_FTDI_HIL is not 1")
	}

	enum := usb.New()
	devs, err := enum.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Skipf(
			"require exactly one supported FTDI attachment; found %d",
			len(devs),
		)
	}
	dev, err := enum.Open(ctx, devs[0])
	if err != nil {
		t.Fatal(err)
	}
	ch, err := ftdi.Open(ctx, dev, ftdi.Config{
		ClockHz:   400_000,
		ProductID: devs[0].PID,
		Port:      ftdi.PortA,
		Interface: ftdi.SWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ch.Close(); err != nil {
			t.Errorf("close FTDI channel: %v", err)
		}
	})

	conn := swd.New(ch)
	if err := conn.JTAGToSWD(ctx); err != nil {
		t.Fatal(err)
	}
	return dap.NewSWDP(conn)
}
