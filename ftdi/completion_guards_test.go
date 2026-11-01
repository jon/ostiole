package ftdi

import (
	"context"
	"errors"
	"testing"
)

type abortDrainClaim struct {
	usbClaim
	out              *fakeUSBTransfer
	cancel           context.CancelFunc
	aborted, drained bool
}

type drainObservedTransfer struct {
	*fakeUSBTransfer
	claim *abortDrainClaim
}

func (t *drainObservedTransfer) Wait(ctx context.Context) (int, error) {
	count, err := t.fakeUSBTransfer.Wait(ctx)
	if ctx.Err() == nil {
		t.claim.drained = true
	}
	return count, err
}

func (c *abortDrainClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	transfer, err := c.usbClaim.SubmitBulk(ctx, endpoint, buffer)
	if err != nil || endpoint&0x80 != 0 {
		return transfer, err
	}
	c.out = &fakeUSBTransfer{done: make(chan struct{})}
	c.cancel()
	return &drainObservedTransfer{fakeUSBTransfer: c.out, claim: c}, nil
}

func (c *abortDrainClaim) AbortBulk(endpoint uint8) error {
	err := c.usbClaim.AbortBulk(endpoint)
	if endpoint&0x80 == 0 && err == nil {
		c.aborted = true
		c.out.complete(0, errors.New("aborted OUT"))
	}
	return err
}

func TestCanceledOUTIsAbortedAndDrained(t *testing.T) {
	c, raw, _ := completionChannel(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	claim := &abortDrainClaim{usbClaim: c.claim, cancel: cancel}
	c.claim = claim
	_, err := c.SWDIO(ctx, []byte{1}, []byte{1}, 1)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrChannelPoisoned) || !claim.aborted || !claim.drained {
		t.Fatalf("OUT cleanup: %v, aborted %v, drained %v", err, claim.aborted, claim.drained)
	}
	before := raw.writesN
	if _, err := c.SWDIO(t.Context(), []byte{1}, []byte{1}, 1); !errors.Is(err, ErrChannelPoisoned) || raw.writesN != before {
		t.Fatalf("replayed accepted OUT: %v", err)
	}
}

func TestMPSSECompletionMarkerIsValidatedAndConsumed(t *testing.T) {
	for _, marker := range []byte{0xab, 0xac} {
		t.Run(map[bool]string{true: "valid", false: "invalid"}[marker == 0xab], func(t *testing.T) {
			c, raw, _ := completionChannel(t)
			raw.mu.Lock()
			raw.readData = [][]byte{{1, 0x60, 0x80, 0xfa, marker}}
			raw.mu.Unlock()
			got, err := c.JTAGIO(t.Context(), []byte{1}, []byte{0}, 1)
			if marker != 0xab {
				if !errors.Is(err, ErrChannelPoisoned) {
					t.Fatalf("bad marker accepted: %v", err)
				}
				return
			}
			if err != nil || len(got) != 1 || got[0] != 1 {
				t.Fatalf("JTAG response includes marker: %x, %v", got, err)
			}
			raw.mu.Lock()
			raw.readData = [][]byte{{1, 0x60, 0xfa, 0xab}}
			raw.mu.Unlock()
			if _, err := c.SWDIO(t.Context(), []byte{1}, []byte{0}, 1); err != nil {
				t.Fatalf("marker left stale payload: %v", err)
			}
		})
	}
}

func TestReceiveRejectsSurplusAndIdlePayload(t *testing.T) {
	c, _, _ := completionChannel(t)
	if err := c.beginResponse(1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.deliverUSBRead([]byte{1, 0x60, 1, 2}); err == nil {
		t.Fatal("accepted oversized response")
	}
	c.endResponse()
	// An exchange rejected before OUT can leave an unused response budget.
	if err := c.beginResponse(2); err != nil {
		t.Fatal(err)
	}
	c.endResponse()
	if _, err := c.deliverUSBRead([]byte{1, 0x60, 1}); err == nil {
		t.Fatal("accepted idle payload")
	}
}

func TestLatePayloadCannotBecomeNextExchange(t *testing.T) {
	c, _, _ := completionChannel(t)
	if err := c.beginResponse(1); err != nil {
		t.Fatal(err)
	}
	c.receive <- []byte{0x11}
	c.endResponse()
	// Retain the defense against payload already queued at the exchange boundary.
	if _, err := c.exchangePayload(t.Context(), []byte{1}, 1); !errors.Is(err, ErrChannelPoisoned) {
		t.Fatalf("accepted stale payload: %v", err)
	}
}

func TestCanceledPreflightAndInvalidResponseBoundsSendNoTraffic(t *testing.T) {
	c, raw, claim := completionChannel(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.SWDIO(ctx, []byte{1}, []byte{1}, 1); !errors.Is(err, context.Canceled) || errors.Is(err, ErrChannelPoisoned) {
		t.Fatalf("canceled preflight: %v", err)
	}
	limitCtx, limitCancel := context.WithCancel(t.Context())
	defer limitCancel()
	claim.waitOUT = func(ctx context.Context, transfer usbBulkTransfer) (int, error) {
		count, err := transfer.Wait(ctx)
		limitCancel()
		return count, err
	}
	for _, size := range []int{-1, maxResponsePayload + 1} {
		if _, err := c.exchangePayload(limitCtx, []byte{1}, size); err == nil {
			t.Fatal("accepted invalid response bound")
		}
	}
	if raw.writesN != 0 || c.restorationFailure() != nil {
		t.Fatal("preflight changed channel")
	}
}
