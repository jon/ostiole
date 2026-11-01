package ftdi

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

type observedOUTClaim struct {
	usbClaim
	rearmed chan struct{}
	waitOUT func(context.Context, usbBulkTransfer) (int, error)
}

func (c *observedOUTClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	transfer, err := c.usbClaim.SubmitBulk(ctx, endpoint, buffer)
	if err != nil {
		return nil, err
	}
	if endpoint&0x80 != 0 {
		select {
		case c.rearmed <- struct{}{}:
		default:
		}
		return transfer, nil
	}
	return &observedOUTTransfer{usbBulkTransfer: transfer, wait: c.waitOUT}, nil
}

type observedOUTTransfer struct {
	usbBulkTransfer
	wait   func(context.Context, usbBulkTransfer) (int, error)
	waited bool
}

func (t *observedOUTTransfer) Wait(ctx context.Context) (int, error) {
	if t.waited {
		return t.usbBulkTransfer.Wait(ctx)
	}
	t.waited = true
	return t.wait(ctx, t.usbBulkTransfer)
}

func completionChannel(t *testing.T) (*Channel, *fakeUSBDevice, *observedOUTClaim) {
	t.Helper()
	raw := &fakeUSBDevice{}
	c, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, c)
	claim := &observedOUTClaim{usbClaim: c.claim, rearmed: make(chan struct{}, 1)}
	claim.waitOUT = func(ctx context.Context, transfer usbBulkTransfer) (int, error) { return transfer.Wait(ctx) }
	c.claim = claim
	return c, raw, claim
}

func TestCancellationAfterOUTAcceptancePoisonsExchange(t *testing.T) {
	c, raw, claim := completionChannel(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	claim.waitOUT = func(ctx context.Context, transfer usbBulkTransfer) (int, error) {
		count, err := transfer.Wait(ctx)
		cancel()
		return count, err
	}
	_, err := c.exchangePayload(ctx, []byte{1}, 1)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrChannelPoisoned) {
		t.Fatalf("accepted canceled exchange: %v", err)
	}
	before := raw.writesN
	if _, err := c.exchangePayload(t.Context(), []byte{2}, 1); !errors.Is(err, ErrChannelPoisoned) || raw.writesN != before {
		t.Fatalf("reused ambiguous stream: %v", err)
	}
}

func TestReceiveRearmsWhileOUTRemainsInFlight(t *testing.T) {
	c, raw, claim := completionChannel(t)
	const packets = 40
	completed := 0
	claim.waitOUT = func(ctx context.Context, transfer usbBulkTransfer) (int, error) {
		for i := range packets {
			for _, packet := range [][]byte{{1, 0x60}, {1, 0x60, byte(i)}} {
				raw.mu.Lock()
				raw.readData = append(raw.readData, packet)
				raw.completeReads()
				raw.mu.Unlock()
				select {
				case <-claim.rearmed:
					completed++
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			}
		}

		return transfer.Wait(ctx)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	got, err := c.exchangePayload(ctx, []byte{1}, packets)
	if err != nil || completed != 2*packets {
		t.Fatalf("OUT blocked receive rearming: completed %d/%d: %v", completed, 2*packets, err)
	}
	for i, value := range got {
		if value != byte(i) {
			t.Fatalf("payload %d = %d", i, value)
		}
	}
}

func TestWriteOnlySWDRequiresTrailingMPSSECompletion(t *testing.T) {
	c, raw, claim := completionChannel(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	claim.waitOUT = func(ctx context.Context, transfer usbBulkTransfer) (int, error) {
		count, err := transfer.Wait(ctx)
		// The USB write completed, but the MPSSE completion reply never arrived.
		cancel()
		return count, err
	}
	_, err := c.SWDIO(ctx, []byte{0xff, 3}, []byte{0x95, 2}, 10)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrChannelPoisoned) {
		t.Errorf("write-only SWD returned before execution confirmation: %v", err)
	}
	raw.mu.Lock()
	commands := append([]byte(nil), raw.writes[0]...)
	raw.mu.Unlock()
	want := []byte{0x80, 2, 3, 0x1b, 7, 0x95, 0x80, 0, 3, 0x1b, 1, 2, 0x80, 0, 1, 0xab, 0x87}
	if !bytes.Equal(commands, want) {
		t.Errorf("trailing host bits or completion marker differ: %x; want %x", commands, want)
	}
}
