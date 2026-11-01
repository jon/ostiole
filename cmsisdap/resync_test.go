package cmsisdap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type queuedPacket struct {
	at   time.Time
	data []byte
}

type reopenStream struct {
	packets                  []queuedPacket
	continuous               bool
	lastPacket, firstCommand time.Time
}

type reopenClaim struct {
	*peerUSBClaim
	stream              *reopenStream
	abortErr, submitErr error
	reads, aborts       int
	onAbort             func()
}

type reopenDevice struct {
	*peerUSBDevice
	queue *reopenClaim
}

func (d *reopenDevice) claimInterface(number uint8) (usbClaim, error) {
	_, err := d.peerUSBDevice.claimInterface(number)
	return d.queue, err
}

func newReopenDevice(stream *reopenStream) *reopenDevice {
	base := metadataPeer()
	return &reopenDevice{peerUSBDevice: base, queue: &reopenClaim{peerUSBClaim: base.claim, stream: stream}}
}

func (c *reopenClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	if endpoint&0x80 != 0 {
		if c.submitErr != nil {
			return nil, c.submitErr
		}
		c.reads++
		transfer, err := c.peerUSBClaim.SubmitBulk(ctx, endpoint, buffer)
		if err != nil {
			return nil, err
		}
		return &reopenRead{claim: c, buffer: buffer, inner: transfer.(*peerBulkTransfer)}, nil
	}
	pending := c.pendingIn
	transfer, err := c.peerUSBClaim.SubmitBulk(ctx, endpoint, buffer)
	if err != nil {
		return nil, err
	}
	if c.stream.firstCommand.IsZero() {
		c.stream.firstCommand = time.Now()
	}
	at := time.Now()
	if n := len(c.stream.packets); n != 0 && c.stream.packets[n-1].at.After(at) {
		at = c.stream.packets[n-1].at.Add(time.Millisecond)
	}
	c.stream.packets = append(c.stream.packets, queuedPacket{at: at, data: append([]byte(nil), pending.buffer[:pending.count]...)})
	pending.pending = true
	c.pendingIn = pending
	return transfer, nil
}

func (c *reopenClaim) AbortBulk(endpoint uint8) error {
	c.aborts++
	if c.onAbort != nil {
		c.onAbort()
	}
	return errors.Join(c.peerUSBClaim.AbortBulk(endpoint), c.abortErr)
}

type reopenRead struct {
	inner  *peerBulkTransfer
	claim  *reopenClaim
	buffer []byte
}

func (r *reopenRead) Wait(ctx context.Context) (int, error) {
	if !r.inner.pending {
		return r.inner.Wait(ctx)
	}
	stream := r.claim.stream
	if len(stream.packets) == 0 && !stream.continuous {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	packet := queuedPacket{at: time.Now().Add(10 * time.Millisecond), data: []byte{0x55}}
	if !stream.continuous {
		packet = stream.packets[0]
	}
	timer := time.NewTimer(time.Until(packet.at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-timer.C:
	}
	if !stream.continuous {
		stream.packets = stream.packets[1:]
	}
	stream.lastPacket = time.Now()
	r.inner.count = copy(r.buffer, packet.data)
	r.inner.pending = false
	return r.inner.count, nil
}

func TestOpenDrainsRepliesDelayedAcrossCloseAndReopen(t *testing.T) {
	stream := &reopenStream{}
	first := newReopenDevice(stream)
	session, err := openSession(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	stream.packets = []queuedPacket{{at: start.Add(10 * time.Millisecond), data: []byte{0x55}}, {at: start.Add(40 * time.Millisecond), data: []byte{0x56}}}
	stream.firstCommand = time.Time{}
	second := newReopenDevice(stream)
	session, err = openSession(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionInfo(t, session.Info(), second.identity)
	if stream.firstCommand.Sub(start) < 140*time.Millisecond || second.queue.reads < 3 || second.queue.aborts != 1 {
		t.Fatalf("reopen skipped quiet interval: first command=%v reads=%d aborts=%d", stream.firstCommand.Sub(start), second.queue.reads, second.queue.aborts)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsContinuousStaleInputBeforeMetadata(t *testing.T) {
	stream := &reopenStream{continuous: true}
	device := newReopenDevice(stream)
	start := time.Now()
	session, err := openSession(t.Context(), device)
	if session != nil || err == nil || !strings.Contains(err.Error(), "resynchron") || !errors.Is(err, context.DeadlineExceeded) || !stream.firstCommand.IsZero() {
		t.Fatalf("continuous input open=(%v,%v), first command=%v", session, err, stream.firstCommand)
	}
	if elapsed := time.Since(start); elapsed < time.Second || elapsed > 2*time.Second {
		t.Fatalf("resynchronization elapsed=%v", elapsed)
	}
	if device.queue.closes != 1 || device.closes != 1 {
		t.Fatal("failed synchronization lost cleanup")
	}
}

func TestOpenCancellationNearQuietThresholdDoesNotSendMetadata(t *testing.T) {
	stream := &reopenStream{}
	device := newReopenDevice(stream)
	ctx, cancel := context.WithTimeout(t.Context(), 95*time.Millisecond)
	defer cancel()
	session, err := openSession(ctx, device)
	if session != nil || !errors.Is(err, context.DeadlineExceeded) || !stream.firstCommand.IsZero() || device.queue.aborts != 1 {
		t.Fatalf("canceled synchronization=(%v,%v), commands=%v aborts=%d", session, err, stream.firstCommand, device.queue.aborts)
	}
}

func TestOpenSynchronizationPreservesSubmissionAndAbortFailures(t *testing.T) {
	for _, submit := range []bool{false, true} {
		device := newReopenDevice(&reopenStream{})
		failure := errors.New("synchronization USB failure")
		if submit {
			device.queue.submitErr = failure
		} else {
			device.queue.abortErr = failure
		}
		session, err := openSession(t.Context(), device)
		if session != nil || !errors.Is(err, failure) || !strings.Contains(err.Error(), "resynchron") || !device.queue.stream.firstCommand.IsZero() || device.queue.closes != 1 || device.closes != 1 {
			t.Fatalf("failed synchronization=(%v,%v), closes=%d/%d", session, err, device.queue.closes, device.closes)
		}
	}
}

func TestOpenCancellationDuringQuietAbortDoesNotSendMetadata(t *testing.T) {
	device := newReopenDevice(&reopenStream{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	device.queue.onAbort = cancel
	session, err := openSession(ctx, device)
	if session != nil || !errors.Is(err, context.Canceled) || !device.queue.stream.firstCommand.IsZero() || device.queue.aborts != 1 {
		t.Fatalf("cancellation during quiet completion=(%v,%v), commands=%v aborts=%d", session, err, device.queue.stream.firstCommand, device.queue.aborts)
	}
}

func TestSynchronizationRejectsInvalidCompletionAndUnelapsedTimeout(t *testing.T) {
	for _, input := range []*peerBulkTransfer{{count: -1}, {count: 65}, {err: context.DeadlineExceeded}, {err: errors.Join(context.DeadlineExceeded, errors.New("host read failed"))}} {
		claim := &scriptedUSBClaim{input: input}
		session := transportSession(claim)
		session.packetSize = 64
		if err := session.synchronizeInput(t.Context()); err == nil || !strings.Contains(err.Error(), "resynchron") {
			t.Fatalf("invalid completion accepted: %+v, error=%v", input, err)
		}
	}
}

func TestSynchronizationHonorsAlreadyCanceledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	claim := &scriptedUSBClaim{input: &peerBulkTransfer{pending: true}}
	session := transportSession(claim)
	if err := session.synchronizeInput(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled synchronization returned %v", err)
	}
}

func (r *reopenRead) Aborted() bool { return r.inner.Aborted() }

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
			claim := &quietCompletionClaim{peerUSBClaim: metadataPeer().claim, transfer: &quietCompletion{count: test.count, err: test.err, aborted: test.aborted}}
			session := &Session{claim: claim, packetSize: 64}
			quiet, err := session.waitForInputQuiet(t.Context())
			if quiet != test.quiet || test.failed && !errors.Is(err, test.err) || !test.failed && err != nil {
				t.Fatalf("quiet=%t want%t completionerror=%v", quiet, test.quiet, err)
			}
		})
	}
}

func TestQuietReadRejectsPrematureHostDeadlineWithAbort(t *testing.T) {
	claim := &quietCompletionClaim{peerUSBClaim: metadataPeer().claim, transfer: &quietCompletion{firstErr: context.DeadlineExceeded, err: context.Canceled, aborted: true}}
	session := &Session{claim: claim, packetSize: 64}
	if quiet, err := session.waitForInputQuiet(t.Context()); quiet || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("premature host deadline accepted as quiet: %t,%v", quiet, err)
	}
}
