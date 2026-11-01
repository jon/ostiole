package cmsisdap

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/usb"
)

type lifecycleClaim struct {
	*peerUSBClaim
	failEndpoint uint8
	submitErr    error
	submissions  int
	pending      bool
	releaseError bool
}

func (c *lifecycleClaim) CleanupPending() bool { return c.pending }
func (c *lifecycleClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	c.submissions++
	if endpoint == c.failEndpoint && c.submitErr != nil {
		return nil, c.submitErr
	}
	return c.peerUSBClaim.SubmitBulk(ctx, endpoint, buffer)
}
func (c *lifecycleClaim) AbortBulk(endpoint uint8) error {
	return c.peerUSBClaim.AbortBulk(endpoint)
}
func (c *lifecycleClaim) Close() error {
	err := c.peerUSBClaim.Close()
	if err == nil || c.releaseError {
		c.pending = false
	}
	return err
}

type lifecycleDevice struct {
	*peerUSBDevice
	claim        *lifecycleClaim
	pending      bool
	releaseError bool
}

func (d *lifecycleDevice) claimInterface(number uint8) (usbClaim, error) {
	if _, err := d.peerUSBDevice.claimInterface(number); err != nil {
		return nil, err
	}
	return d.claim, nil
}
func (d *lifecycleDevice) CleanupPending() bool { return d.pending }
func (d *lifecycleDevice) Close() error {
	err := d.peerUSBDevice.Close()
	if err == nil || d.releaseError {
		d.pending = false
	}
	return err
}

func openLifecycleSession(t *testing.T) (*Session, *lifecycleDevice) {
	t.Helper()
	peer := newSWDCommandPeer(64)
	claim := &lifecycleClaim{peerUSBClaim: peer.device.claim, pending: true}
	device := &lifecycleDevice{peerUSBDevice: peer.device, claim: claim, pending: true}
	session, err := openSession(t.Context(), device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	return session, device
}

func TestCloseAfterGoneSubmissionReleasesHostResources(t *testing.T) {
	for _, endpoint := range []uint8{0x82, 0x01} {
		t.Run(map[uint8]string{0x82: "IN", 0x01: "OUT"}[endpoint], func(t *testing.T) {
			session, device := openLifecycleSession(t)
			claim := device.claim
			operationErr := errors.New("disconnect submission lost device")
			claimErr, deviceErr := errors.New("final claim loss"), errors.New("final device loss")
			claim.failEndpoint, claim.submitErr = endpoint, errors.Join(usb.ErrDeviceGone, operationErr)
			claim.closeErrs, claim.releaseError = []error{claimErr}, true
			device.closeErrs, device.releaseError = []error{deviceErr}, true
			err := session.Close()
			if !errors.Is(err, operationErr) || !errors.Is(err, usb.ErrDeviceGone) || !errors.Is(err, claimErr) || !errors.Is(err, deviceErr) {
				t.Fatalf("Close() lost operation or cleanup errors: %v", err)
			}
			if claim.closes != 1 || device.closes != 1 || claim.pending || device.pending {
				t.Fatalf("terminal cleanup retained resources: claim=%d/%t device=%d/%t", claim.closes, claim.pending, device.closes, device.pending)
			}
			submissions := claim.submissions
			if err := session.Close(); err != nil || claim.submissions != submissions || claim.closes != 1 || device.closes != 1 {
				t.Fatalf("completed cleanup repeated traffic: %v", err)
			}
		})
	}
}

func TestUnsentLiveFailureRetainsSessionForUsefulRetry(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded, errors.New("live submission failure")} {
		session, device := openLifecycleSession(t)
		claim := device.claim
		claim.failEndpoint, claim.submitErr = 0x82, want
		if err := session.Close(); !errors.Is(err, want) || !session.RestorationPossible() || !session.CleanupPending() {
			t.Fatalf("live failure abandoned restoration: %v", err)
		}
		if claim.closes != 0 || device.closes != 0 {
			t.Fatal("live failure released host resources")
		}
		claim.submitErr = nil
		if err := session.Close(); err != nil || session.CleanupPending() || session.RestorationPossible() {
			t.Fatalf("useful retry = %v", err)
		}
		if claim.closes != 1 || device.closes != 1 {
			t.Fatal("retry did not release resources once")
		}
	}
}

func TestSessionRetainsLiveHostCleanupAfterTerminalLoss(t *testing.T) {
	session, device := openLifecycleSession(t)
	claim := device.claim
	claim.failEndpoint, claim.submitErr = 0x82, usb.ErrDeviceGone
	live := errors.New("host cleanup remains pending")
	claim.closeErrs = []error{live}
	if err := session.Close(); !errors.Is(err, usb.ErrDeviceGone) || !errors.Is(err, live) {
		t.Fatalf("initial close = %v", err)
	}
	if session.RestorationPossible() || !session.CleanupPending() || device.closes != 0 {
		t.Fatal("terminal restoration confused with host ownership")
	}
	submissions := claim.submissions
	device.closeErrs = []error{live}
	if err := session.Close(); !errors.Is(err, live) || !session.CleanupPending() || claim.submissions != submissions {
		t.Fatalf("device retry failed to retain cleanup: %v", err)
	}
	if err := session.Close(); !errors.Is(err, usb.ErrDeviceGone) || session.CleanupPending() {
		t.Fatalf("final host retry lost restoration outcome: %v", err)
	}
	if err := session.Close(); err != nil || device.closes != 2 || claim.closes != 2 || claim.submissions != submissions {
		t.Fatalf("released resources retried: %v", err)
	}
}

type legacyLifecycleDevice struct{ usbDevice }

func TestSessionLegacyCleanupFailureStaysConservative(t *testing.T) {
	session, device := openLifecycleSession(t)
	session.device = legacyLifecycleDevice{device}
	want := errors.New("unknown device cleanup state")
	device.closeErrs, device.releaseError = []error{want}, true
	if err := session.Close(); !errors.Is(err, want) || !session.CleanupPending() {
		t.Fatalf("legacy failure not retained: %v", err)
	}
	if err := session.Close(); err != nil || session.CleanupPending() || device.closes != 2 {
		t.Fatalf("legacy cleanup retry = %v, calls=%d", err, device.closes)
	}
}

func TestFailedOpenRetainsLiveHostCleanup(t *testing.T) {
	peer := newSWDCommandPeer(64)
	peer.clockStatus = statusError
	want := errors.New("live claim cleanup failure")
	claim := &lifecycleClaim{peerUSBClaim: peer.device.claim, pending: true}
	claim.closeErrs = []error{want}
	device := &lifecycleDevice{peerUSBDevice: peer.device, claim: claim, pending: true}
	session, err := openSession(t.Context(), device, WithSWD(100_000))
	if session == nil || !errors.Is(err, want) || !session.CleanupPending() || device.closes != 0 {
		t.Fatalf("failed open lost cleanup owner: session=%v err=%v", session, err)
	}
	commands := len(peer.effectCommands())
	if err := session.Close(); err != nil || session.CleanupPending() || len(peer.effectCommands()) != commands {
		t.Fatalf("failed-open host retry sent target traffic: %v", err)
	}
}

func TestSessionStateQueriesAndKnownLossPreventTraffic(t *testing.T) {
	for _, session := range []*Session{nil, {}} {
		if session.RestorationPossible() || session.CleanupPending() {
			t.Fatal("empty session retained restoration or cleanup")
		}
	}
	session, device := openLifecycleSession(t)
	claim := device.claim
	before := claim.submissions
	if !session.RestorationPossible() || !session.CleanupPending() || claim.submissions != before {
		t.Fatal("healthy queries changed traffic or lost ownership")
	}
	claim.failEndpoint, claim.submitErr = 0x82, usb.ErrDeviceGone
	if err := session.ConfigureSWD(t.Context(), 200_000); !errors.Is(err, usb.ErrDeviceGone) {
		t.Fatalf("known loss = %v", err)
	}
	before = claim.submissions
	if session.RestorationPossible() || !session.CleanupPending() {
		t.Fatal("loss did not distinguish restoration from host cleanup")
	}
	if _, err := session.SWDIO(t.Context(), []byte{1}, []byte{1}, 1); !errors.Is(err, usb.ErrDeviceGone) || claim.submissions != before {
		t.Fatalf("known loss replayed traffic: %v", err)
	}
	if err := session.Close(); !errors.Is(err, usb.ErrDeviceGone) || session.CleanupPending() || claim.submissions != before {
		t.Fatalf("loss cleanup sent target traffic: %v", err)
	}
}
