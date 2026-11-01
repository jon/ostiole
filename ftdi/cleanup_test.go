package ftdi

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/usb"
)

type cleanupDevice struct {
	*fakeUSBDevice
	controlErr error
	failAt     int
	closeErr   error
	pending    bool
	closes     int
	owner      *cleanupClaim
}

type cleanupClaim struct {
	abortResult error
	*fakeUSBClaim
	closeErr error
	pending  bool
	closes   int
}

func (d *cleanupDevice) claimInterface(iface uint8) (usbClaim, error) {
	claim, err := d.fakeUSBDevice.claimInterface(iface)
	if err != nil {
		return nil, err
	}
	d.owner = &cleanupClaim{fakeUSBClaim: claim.(*fakeUSBClaim), pending: true}
	return d.owner, nil
}

func (d *cleanupDevice) ControlTransfer(ctx context.Context, typ, request uint8, value, index uint16, data []byte) (int, error) {
	n, err := d.fakeUSBDevice.ControlTransfer(ctx, typ, request, value, index, data)
	if len(d.controls) == d.failAt {
		return n, d.controlErr
	}
	return n, err
}

func (d *cleanupDevice) Close() error         { d.closes++; return d.closeErr }
func (d *cleanupDevice) CleanupPending() bool { return d.pending }
func (c *cleanupClaim) Close() error {
	c.closes++
	if c.closeErr == nil || !c.pending {
		c.device.claim = nil
		c.pending = false
	}
	return c.closeErr
}
func (c *cleanupClaim) CleanupPending() bool { return c.pending }

func newCleanupChannel(t *testing.T) (*Channel, *cleanupDevice) {
	t.Helper()
	d := &cleanupDevice{fakeUSBDevice: &fakeUSBDevice{}}
	c, err := newChannel(d, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.claimUSB(); err != nil {
		t.Fatal(err)
	}
	c.active = true
	return c, d
}

func TestCloseRetainsLiveFTDIRestoration(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("live control failure")} {
		for step := 1; step <= 4; step++ {
			checkLiveFTDIRestoration(t, failure, step)
		}
	}
}

func checkLiveFTDIRestoration(t *testing.T, failure error, step int) {
	t.Helper()
	c, d := newCleanupChannel(t)
	d.failAt, d.controlErr = step, failure
	if err := c.Close(); !errors.Is(err, failure) {
		t.Fatalf("step %d: lost control error: %v", step, err)
	}
	if d.owner.closes != 0 || d.closes != 0 {
		t.Fatalf("step %d: released live restoration: claim %d device %d", step, d.owner.closes, d.closes)
	}
	if len(d.controls) != step {
		t.Fatalf("step %d: continued failed restoration: %v", step, d.controls)
	}
	d.failAt = 0
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	want := []controlRecord{{request: 0x0b, value: 0, index: 1}, {request: 0x09, value: 16, index: 1}, {request: 0, value: 1, index: 1}, {request: 0, value: 2, index: 1}}
	if len(d.controls) != step+4 {
		t.Fatalf("step %d: missing restoration retry: %v", step, d.controls)
	}
	for i, record := range want {
		if got := d.controls[step+i]; got != record {
			t.Fatalf("retry step %d: %v want %v", i, got, record)
		}
	}
	if d.owner.closes != 1 || d.closes != 1 {
		t.Fatal("retry did not release host resources once")
	}
	if err := c.Close(); err != nil || d.closes != 1 || len(d.controls) != step+4 {
		t.Fatalf("completed close repeated work: %v", err)
	}
}

func TestCloseAbandonsFTDIRestorationAfterLoss(t *testing.T) {
	for _, poison := range []bool{false, true} {
		c, d := newCleanupChannel(t)
		failure := errors.New("operation failed")
		if poison {
			c.recordPoison(failure)
		} else {
			d.failAt, d.controlErr = 1, errors.Join(failure, usb.ErrDeviceGone)
		}
		err := c.Close()
		if poison && !errors.Is(err, ErrChannelPoisoned) {
			t.Fatalf("lost poisoned restoration: %v", err)
		}
		if !errors.Is(err, failure) {
			t.Fatalf("lost operation: %v", err)
		}
		want := 1
		if poison {
			want = 0
		}
		if len(d.controls) != want || d.owner.closes != 1 || d.closes != 1 {
			t.Fatalf("terminal cleanup: controls %v claim %d device %d", d.controls, d.owner.closes, d.closes)
		}
		if err := c.Close(); err != nil || len(d.controls) != want || d.closes != 1 {
			t.Fatalf("final close: %v", err)
		}
	}
}

func TestCloseRetainsOnlyPendingFTDIHostCleanup(t *testing.T) {
	for _, pending := range []bool{false, true} {
		c, d := newCleanupChannel(t)
		claimErr, deviceErr := errors.New("claim close"), errors.New("device close")
		d.owner.closeErr, d.owner.pending = claimErr, pending
		d.closeErr = deviceErr
		err := c.Close()
		if !errors.Is(err, claimErr) {
			t.Fatalf("lost claim error: %v", err)
		}
		if pending {
			if d.closes != 0 {
				t.Fatal("closed device while live claim retained")
			}
			d.owner.closeErr = nil
			d.pending = true
			if err := c.Close(); !errors.Is(err, deviceErr) {
				t.Fatalf("lost live device error: %v", err)
			}
			d.pending = false
			if err := c.Close(); !errors.Is(err, deviceErr) {
				t.Fatalf("lost final device error: %v", err)
			}
		} else if !errors.Is(err, deviceErr) {
			t.Fatalf("lost final device error: %v", err)
		}
		controls, closes, claims := len(d.controls), d.closes, d.owner.closes
		if err := c.Close(); err != nil || len(d.controls) != controls || d.closes != closes || d.owner.closes != claims {
			t.Fatalf("repeated final cleanup: %v", err)
		}
		if len(d.controls) != 4 {
			t.Fatalf("repeated successful restoration: %v", d.controls)
		}
	}
}

func (c *cleanupClaim) AbortBulk(endpoint uint8) error {
	return errors.Join(c.fakeUSBClaim.AbortBulk(endpoint), c.abortResult)
}

func TestEmptyFTDICleanupStateQueries(t *testing.T) {
	var nilChannel *Channel
	for _, c := range []*Channel{nilChannel, {}} {
		if c.RestorationPossible() || c.CleanupPending() {
			t.Fatal("empty owner reports pending state")
		}
	}
}

func TestFTDICleanupStateQueries(t *testing.T) {
	c, d := newCleanupChannel(t)
	controls, aborts := len(d.controls), d.aborts
	if !c.RestorationPossible() || !c.CleanupPending() {
		t.Fatal("live owner lost state")
	}
	c.recordLoss(context.DeadlineExceeded)
	if !c.RestorationPossible() {
		t.Fatal("timeout authorized abandonment")
	}
	c.recordLoss(usb.ErrDeviceGone)
	if c.RestorationPossible() || !c.CleanupPending() {
		t.Fatal("lost device state")
	}
	if len(d.controls) != controls || d.aborts != aborts {
		t.Fatal("query performed I/O")
	}
	if err := c.Close(); !errors.Is(err, usb.ErrDeviceGone) {
		t.Fatal(err)
	}
	if c.RestorationPossible() || c.CleanupPending() {
		t.Fatal("released owner reports pending state")
	}
	c, d = newCleanupChannel(t)
	c.recordPoison(errors.New("uncertain exchange"))
	if c.RestorationPossible() || !c.CleanupPending() {
		t.Fatal("poisoned owner state")
	}
	if err := c.Close(); !errors.Is(err, ErrChannelPoisoned) {
		t.Fatal(err)
	}
	if d.closes != 1 || c.CleanupPending() {
		t.Fatal("poisoned owner did not release")
	}
}

func TestGoneFTDIReceiveDrainRetainsLiveClaim(t *testing.T) {
	c, d := newCleanupChannel(t)
	if err := c.openUSBTransfers(t.Context()); err != nil {
		t.Fatal(err)
	}
	loss, claimErr := errors.Join(errors.New("lost endpoint"), usb.ErrDeviceGone), errors.New("claim still draining")
	d.owner.abortResult = loss
	d.owner.closeErr = claimErr
	if err := c.Close(); !errors.Is(err, loss) || !errors.Is(err, claimErr) {
		t.Fatalf("lost cleanup errors: %v", err)
	}
	if d.closes != 0 || !c.CleanupPending() || c.RestorationPossible() {
		t.Fatal("lost pending cleanup")
	}
	d.mu.Lock()
	pending := len(d.pendingReads)
	d.mu.Unlock()
	if pending != 0 || len(d.controls) != 0 {
		t.Fatal("did not drain reads or sent terminal restoration")
	}
	d.owner.closeErr, d.owner.abortResult = nil, nil
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.CleanupPending() || d.closes != 1 || d.owner.closes != 2 || len(d.controls) != 0 {
		t.Fatal("failed to finish retained cleanup")
	}
	if err := c.Close(); err != nil || d.owner.closes != 2 || d.closes != 1 {
		t.Fatalf("repeated cleanup: %v", err)
	}
}

type legacyCleanupDevice struct{ usbDevice }

func TestUnknownFTDIHostCleanupRemainsConservative(t *testing.T) {
	c, d := newCleanupChannel(t)
	c.device = legacyCleanupDevice{d}
	d.closeErr = errors.New("legacy close")
	if err := c.Close(); !errors.Is(err, d.closeErr) {
		t.Fatal(err)
	}
	if !c.CleanupPending() || d.closes != 1 {
		t.Fatal("lost unknown owner")
	}
	d.closeErr = nil
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.CleanupPending() || d.closes != 2 || len(d.controls) != 4 {
		t.Fatal("legacy retry repeated restoration or retained resources")
	}
}
