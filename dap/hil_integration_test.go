//go:build integration

package dap_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

type ackCountingWire struct {
	inner  swd.Wire
	counts [8]int
}

type parityFaultWire struct {
	inner swd.Wire
	armed bool
}

func (w *parityFaultWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if w.armed && bits == 42 {
		w.armed = false
		corrupt := append([]byte(nil), output...)
		corrupt[33/8] ^= 1 << (33 % 8)
		output = corrupt
	}
	return w.inner.SWDIO(ctx, direction, output, bits)
}

func (w *ackCountingWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	if err == nil && bits == 12 && len(input) >= 2 {
		var ack byte
		for bit := range 3 {
			if input[(9+bit)/8]>>(uint(9+bit)%8)&1 != 0 {
				ack |= 1 << uint(bit)
			}
		}
		w.counts[ack]++
	}
	return input, err
}

func openHardwareDebugPort(t *testing.T, ctx context.Context) *dap.DebugPort {
	dp, _ := openHardwareDebugPortWithFaultWire(t, ctx)
	return dp
}

func openHardwareDebugPortWithFaultWire(t *testing.T, ctx context.Context) (*dap.DebugPort, *parityFaultWire) {
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
		t.Skipf("require exactly one supported FTDI attachment; found %d", len(devs))
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
		t.Fatal(errors.Join(err, dev.Close()))
	}
	t.Cleanup(func() {
		if err := ch.Close(); err != nil {
			t.Errorf("close FTDI channel: %v", err)
		}
	})

	wire := &ackCountingWire{inner: ch}
	faultWire := &parityFaultWire{inner: wire}
	conn := swd.New(faultWire)
	if err := conn.JTAGToSWD(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		invalid := 0
		for ack, count := range wire.counts {
			if ack != 0b001 && ack != 0b010 && ack != 0b100 {
				invalid += count
			}
		}
		t.Logf("physical ACKs: OK=%d WAIT=%d FAULT=%d invalid=%d",
			wire.counts[0b001], wire.counts[0b010], wire.counts[0b100], invalid)
	})
	return dap.NewSWDP(conn), faultWire
}
