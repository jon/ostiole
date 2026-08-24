//go:build integration

package dap_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

type ackCountingWire struct {
	inner      swd.Wire
	counts     [8]int
	entries    int
	calls      int
	fixedCalls int
	fixed      int
}

type parityFaultWire struct {
	inner swd.Wire
	armed bool
}

func (w *ackCountingWire) MaxTransferBits() int { return wireTransferLimit(w.inner) }

func (w *parityFaultWire) MaxTransferBits() int { return wireTransferLimit(w.inner) }

func wireTransferLimit(w swd.Wire) int {
	if limits, ok := w.(swd.TransferLimits); ok {
		return limits.MaxTransferBits()
	}
	return 54
}

func (w *parityFaultWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if w.armed && (bits == 42 || bits == 54) {
		w.armed = false
		corrupt := append([]byte(nil), output...)
		parityBit := 33
		if bits == 54 {
			parityBit = 45
		}
		corrupt[parityBit/8] ^= 1 << (parityBit % 8)
		output = corrupt
	}
	return w.inner.SWDIO(ctx, direction, output, bits)
}

func (w *ackCountingWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	if err == nil && bits == 136 {
		w.entries++
	}
	if err == nil && bits == 12 && len(input) >= 2 {
		w.counts[ackAt(input, 9)]++
	}
	if err == nil && bits >= 54 && bits%54 == 0 {
		w.fixedCalls++
		w.fixed += bits / 54
		for offset := 0; offset < bits; offset += 54 {
			w.counts[ackAt(input, offset+9)]++
		}
	}
	return input, err
}

func ackAt(input []byte, offset int) byte {
	var ack byte
	for bit := range 3 {
		if input[(offset+bit)/8]>>(uint(offset+bit)%8)&1 != 0 {
			ack |= 1 << uint(bit)
		}
	}
	return ack
}

func openHardwareDebugPort(t *testing.T, ctx context.Context) *dap.DebugPort {
	dp, _ := openHardwareDebugPortWithFaultWire(t, ctx)
	return dp
}

func releaseHardwareDebugPort(t *testing.T, dp *dap.DebugPort) {
	t.Helper()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
	defer cleanupCancel()
	if err := releaseHardwareDebugPortWithin(cleanupCtx, dp); err != nil {
		t.Errorf("release SW-DP within cleanup deadline: %v", err)
	}
}

func releaseHardwareDebugPortWithin(ctx context.Context, dp *dap.DebugPort) error {
	for {
		err := dp.Release(ctx)
		if err == nil {
			return nil
		}
		if errors.Is(err, ftdi.ErrChannelPoisoned) {
			return err
		}
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
	}
}

func releaseHardwareMemAP(t *testing.T, mem *dap.MemAP) {
	t.Helper()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
	defer cleanupCancel()
	if err := releaseHardwareMemAPWithin(cleanupCtx, mem); err != nil {
		t.Errorf("release MEM-AP within cleanup deadline: %v", err)
	}
}

func releaseHardwareMemAPWithin(ctx context.Context, mem *dap.MemAP) error {
	for {
		err := mem.Release(ctx)
		if err == nil {
			return nil
		}
		if errors.Is(err, ftdi.ErrChannelPoisoned) {
			return err
		}
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
	}
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
	ch, err := ftdi.Open(ctx, dev, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 400_000})
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
	t.Cleanup(func() {
		invalid := 0
		for ack, count := range wire.counts {
			if ack != 0b001 && ack != 0b010 && ack != 0b100 {
				invalid += count
			}
		}
		t.Logf("SWD entries=%d SWDIO_calls=%d physical ACKs: OK=%d WAIT=%d FAULT=%d invalid=%d fixed_calls=%d fixed_frames=%d", wire.entries, wire.calls, wire.counts[0b001], wire.counts[0b010], wire.counts[0b100], invalid, wire.fixedCalls, wire.fixed)
	})
	return dap.NewDebugPort(conn), faultWire
}
