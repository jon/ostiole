//go:build integration

package swd_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

func TestReadDPIDROverFTDI(t *testing.T) {
	if os.Getenv("OSTIOLE_FTDI_HIL") != "1" {
		t.Skip("OSTIOLE_FTDI_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	bus := usb.New()
	devs, err := bus.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Skipf("require exactly one supported FTDI attachment; found %d", len(devs))
	}
	dev, err := bus.Open(ctx, devs[0])
	if err != nil {
		t.Fatal(err)
	}
	ch, err := ftdi.Open(ctx, dev, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(errors.Join(err, dev.Close()))
	}
	t.Cleanup(func() {
		if err := ch.Close(); err != nil {
			t.Errorf("close FTDI channel: %v", err)
		}
	})

	conn := swd.New(ch)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := conn.Release(cleanupCtx); err != nil {
			t.Errorf("release SWD connection: %v", err)
		}
	})
	dpidr, err := conn.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dpidr == 0 || dpidr == ^uint32(0) || dpidr&1 == 0 {
		t.Fatalf("DPIDR = %#08x", dpidr)
	}
	t.Logf("DPIDR=%#08x", dpidr)
}
