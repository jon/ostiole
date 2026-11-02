package jlink

import (
	"context"
	"errors"
	"github.com/jon/ostiole/usb"
	"strings"
	"testing"
	"time"
)

type resyncDevice struct {
	*peerUSBDevice
	delay                   time.Duration
	continuous              bool
	submissionErr, abortErr error
	drainReads              int
}

type resyncClaim struct {
	usbClaim
	device   *resyncDevice
	draining bool
	pending  *resyncTransfer
}

type resyncTransfer struct {
	ready         func() (int, error)
	at            time.Time
	done, aborted bool
	count         int
	err           error
}

func (d *resyncDevice) claimInterface(number uint8) (usbClaim, error) {
	claim, err := d.peerUSBDevice.claimInterface(number)
	if err != nil {
		return nil, err
	}
	return &resyncClaim{usbClaim: claim, device: d, draining: true}, nil
}

func (c *resyncClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	if endpoint&0x80 == 0 {
		c.draining = false
	}
	if !c.draining {
		return c.usbClaim.SubmitBulk(ctx, endpoint, buffer)
	}
	d := c.device
	d.drainReads++
	if d.submissionErr != nil {
		return nil, d.submissionErr
	}
	transfer := &resyncTransfer{at: time.Now().Add(d.delay)}
	if d.continuous {
		transfer.ready = func() (int, error) { buffer[0] = 0xa5; return 1, nil }
	} else if len(d.responses) != 0 {
		transfer.ready = func() (int, error) { return d.read(buffer) }
	}
	c.pending = transfer
	return transfer, nil
}

func (c *resyncClaim) AbortBulk(endpoint uint8) error {
	if c.pending != nil && !c.pending.done {
		if c.device.abortErr != nil {
			return c.device.abortErr
		}
		c.pending.done, c.pending.aborted, c.pending.err = true, true, context.Canceled
	}
	return c.usbClaim.AbortBulk(endpoint)
}

func (t *resyncTransfer) Wait(ctx context.Context) (int, error) {
	if t.done {
		return t.count, t.err
	}
	if t.ready == nil {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	timer := time.NewTimer(time.Until(t.at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-timer.C:
		t.count, t.err = t.ready()
		t.done = true
		return t.count, t.err
	}
}

func (t *resyncTransfer) Aborted() bool { return t.done && t.aborted }

func resyncMetadata(record byte) []peerOperation {
	return []peerOperation{
		{request: []byte{1}, response: [][]byte{{2, 0}, {record, 0}}},
		{request: []byte{0xe8}, response: [][]byte{{0, 0, 0, 0}}},
	}
}

func TestOpenDrainsAbandonedRepliesAcrossCloseAndReopen(t *testing.T) {
	lifetime, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	raw := metadataPeer(t, resyncMetadata('o'))
	device := &resyncDevice{peerUSBDevice: raw, delay: 35 * time.Millisecond}
	session, err := openSession(lifetime, device)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(lifetime)
	raw.operations = append(raw.operations, peerOperation{request: []byte{0xe8}, response: [][]byte{{0, 0, 0, 0}, {0xa5}}})
	raw.afterWrite = cancel
	if _, err = session.exchange(ctx, []byte{0xe8}, 4); !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("abandoned reply did not poison session: %v", err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	raw.afterWrite = nil
	raw.operations = resyncMetadata('n')
	before := device.drainReads
	session, err = openSession(lifetime, device)
	if err != nil {
		t.Fatalf("reopen did not drain old replies: %v", err)
	}
	if session.Info().Firmware != "n" || device.drainReads-before != 3 {
		t.Fatalf("reopen metadata=%q drainreads=%d", session.Info().Firmware, device.drainReads-before)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsContinuousStaleInputBeforeMetadata(t *testing.T) {
	raw := metadataPeer(t, resyncMetadata('n'))
	device := &resyncDevice{peerUSBDevice: raw, continuous: true, delay: time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	session, err := openSession(ctx, device)
	elapsed := time.Since(started)
	if elapsed < 900*time.Millisecond || elapsed > 1900*time.Millisecond {
		t.Fatalf("synchronization elapsed %s, want bounded one-second budget", elapsed)
	}
	if session != nil || err == nil || !strings.Contains(err.Error(), "resynchronize") || !errors.Is(err, context.DeadlineExceeded) || raw.writes != 0 {
		t.Fatalf("continuous input: session=%v error=%v writes=%d", session, err, raw.writes)
	}
}

func TestOpenCancellationDuringQuietDoesNotSendMetadata(t *testing.T) {
	raw := metadataPeer(t, resyncMetadata('n'))
	device := &resyncDevice{peerUSBDevice: raw}
	ctx, cancel := context.WithTimeout(t.Context(), 95*time.Millisecond)
	defer cancel()
	session, err := openSession(ctx, device)
	if session != nil || !errors.Is(err, context.DeadlineExceeded) || raw.writes != 0 || raw.closes != 1 {
		t.Fatalf("quiet cancellation: session=%v error=%v writes=%d close=%d", session, err, raw.writes, raw.closes)
	}
}

func TestOpenResynchronizationRetainsSubmissionAndAbortErrors(t *testing.T) {
	for _, abort := range []bool{false, true} {
		cause := errors.New("host failure")
		raw := metadataPeer(t, resyncMetadata('n'))
		device := &resyncDevice{peerUSBDevice: raw}
		if abort {
			device.abortErr = cause
		} else {
			device.submissionErr = cause
		}
		session, err := openSession(t.Context(), device)
		if session != nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "resynchronize") || raw.writes != 0 || raw.closes != 1 {
			t.Fatalf("sync failure abort=%t: session=%v error=%v writes=%d close=%d", abort, session, err, raw.writes, raw.closes)
		}
	}
}

type quietCompletion struct {
	firstErr error
	complete bool
	count    int
	err      error
	aborted  bool
}

func (r *quietCompletion) Wait(ctx context.Context) (int, error) {
	if r.complete {
		return r.count, r.err
	}
	if r.firstErr != nil {
		r.complete = true
		return 0, r.firstErr
	}
	<-ctx.Done()
	r.complete = true
	return 0, ctx.Err()
}
func (r *quietCompletion) Aborted() bool { return r.complete && r.aborted }

type quietCompletionClaim struct {
	*peerUSBClaim
	transfer *quietCompletion
}

func (c *quietCompletionClaim) SubmitBulk(context.Context, uint8, []byte) (usbBulkTransfer, error) {
	return c.transfer, nil
}
func (c *quietCompletionClaim) AbortBulk(uint8) error { return nil }

func TestQuietTimeoutObservesFinalDrainedCompletion(t *testing.T) {
	failure := errors.New("unrelated native read failure")
	for _, test := range []struct {
		name                   string
		count                  int
		err                    error
		aborted, quiet, failed bool
	}{{"packet", 1, nil, false, false, false}, {"empty packet", 0, nil, false, false, false}, {"aborted", 0, context.Canceled, true, true, false}, {"native failure", 0, failure, false, false, true}, {"partial abort", 0, context.Canceled, false, false, true}} {
		t.Run(test.name, func(t *testing.T) {
			claim := &quietCompletionClaim{peerUSBClaim: &peerUSBClaim{device: metadataPeer(t, nil)}, transfer: &quietCompletion{count: test.count, err: test.err, aborted: test.aborted}}
			session := &Session{claim: claim, application: applicationInterface{bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 64}}}
			quiet, err := session.waitForInputQuiet(t.Context())
			if quiet != test.quiet || test.failed && !errors.Is(err, test.err) || !test.failed && err != nil {
				t.Fatalf("quiet=%t want%t completionerror=%v", quiet, test.quiet, err)
			}
		})
	}
}

func TestQuietReadRejectsPrematureHostDeadlineWithAbort(t *testing.T) {
	claim := &quietCompletionClaim{peerUSBClaim: &peerUSBClaim{device: metadataPeer(t, nil)}, transfer: &quietCompletion{firstErr: context.DeadlineExceeded, err: context.Canceled, aborted: true}}
	session := &Session{claim: claim, application: applicationInterface{bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 64}}}
	if quiet, err := session.waitForInputQuiet(t.Context()); quiet || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("premature host deadline accepted as quiet: %t,%v", quiet, err)
	}
}

func TestOpenCancellationStopsContinuousDrainPromptly(t *testing.T) {
	raw := metadataPeer(t, resyncMetadata('n'))
	device := &resyncDevice{peerUSBDevice: raw, continuous: true, delay: time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	session, err := openSession(ctx, device)
	if session != nil || !errors.Is(err, context.DeadlineExceeded) || raw.writes != 0 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("continuous cancellation: session=%v error=%v writes=%d elapsed=%s", session, err, raw.writes, time.Since(started))
	}
}
