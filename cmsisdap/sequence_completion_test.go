package cmsisdap

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type recordedSWDStream struct {
	swd.Wire
	direction, output []byte
	bits              int
}

func (w *recordedSWDStream) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.direction = append([]byte(nil), direction...)
	w.output = append([]byte(nil), output...)
	w.bits = bits
	return w.Wire.SWDIO(ctx, direction, output, bits)
}

func TestSWDIOCancellationFinishesPackedWrite(t *testing.T) {
	wire := &recordedSWDStream{Wire: swdsim.New(dapsim.New(0x2ba01477))}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteDP(t.Context(), 0, 0x1e); err != nil {
		t.Fatal(err)
	}
	direction, output, bits := wire.direction, wire.output, wire.bits
	if err := conn.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	peer := newSWDSequencePeer(8)
	peer.device.claim.responses[infoFirmwareVersion] = infoResponse([]byte("1.2.3\x00"))
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: commandSWDSequence, accepted: cancel}
	_, err = session.SWDIO(ctx, direction, output, bits)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SWDIO error = %v", err)
	}
	if len(peer.sequenceCommands) < 2 || peer.cycles != bits || session.poisoned {
		t.Fatalf("commands %d clocks %d/%d poisoned %t", len(peer.sequenceCommands), peer.cycles, bits, session.poisoned)
	}
	if !reflect.DeepEqual(peer.direction, testBits(direction, bits)) {
		t.Fatal("write direction changed")
	}
	for bit := range bits {
		if peer.output[bit] != (testBit(direction, bit) && testBit(output, bit)) {
			t.Fatalf("write data changed at bit %d", bit)
		}
	}
}

type acceptanceClaim struct {
	*peerUSBClaim
	command       byte
	accepted      context.CancelFunc
	check         func(context.Context, uint8) error
	cancelAfterIN context.CancelFunc
}

func (c *acceptanceClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transfer, err := c.peerUSBClaim.SubmitBulk(ctx, endpoint, buffer)
	if err == nil && endpoint&0x80 != 0 && c.cancelAfterIN != nil {
		c.cancelAfterIN()
		c.cancelAfterIN = nil
	}
	if err == nil && endpoint&0x80 == 0 && buffer[0] == c.command && c.accepted != nil {
		c.accepted()
		c.accepted = nil
	}
	if err != nil {
		return nil, err
	}
	return &completionTransfer{inner: transfer, check: func(ctx context.Context) error {
		if c.check != nil {
			if err := c.check(ctx, endpoint); err != nil {
				return err
			}
		}
		return ctx.Err()
	}}, nil
}

type completionTransfer struct {
	inner usbBulkTransfer
	check func(context.Context) error
}

func (t *completionTransfer) Wait(ctx context.Context) (int, error) {
	if err := t.check(ctx); err != nil {
		return 0, err
	}
	return t.inner.Wait(ctx)
}

func TestConfigureSWDObservesCompletedClockBeforeCancellation(t *testing.T) {
	peer := newSWDSequencePeer(64)
	session, err := openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: commandSWJClock, accepted: cancel}
	err = session.ConfigureSWD(ctx, 100_000)
	if !errors.Is(err, context.Canceled) || session.MaxClockHz() != 100_000 || session.poisoned {
		t.Fatalf("ConfigureSWD = %v, clock %d, poisoned %t", err, session.MaxClockHz(), session.poisoned)
	}
}

func TestOpenReportsCancellationAfterFinalMetadata(t *testing.T) {
	peer := newSWDSequencePeer(64)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	peer.device.claim.handle = func(request []byte) ([]byte, error) {
		response, err := peer.handle(request)
		if len(request) == 2 && request[0] == commandInfo && request[1] == infoFirmwareVersion {
			cancel()
		}
		return response, err
	}
	session, err := openSession(ctx, peer.device)
	if !errors.Is(err, context.Canceled) || session != nil || peer.device.closes != 1 || peer.device.claim.closes != 1 {
		t.Fatalf("Open = %v %v; closes %d/%d", session, err, peer.device.closes, peer.device.claim.closes)
	}
}

func TestSWDIOCompletionBudget(t *testing.T) {
	for _, budget := range []time.Duration{time.Second, 2 * time.Second} {
		t.Run(budget.String(), func(t *testing.T) {
			peer := newSWDSequencePeer(16)
			var options []Option
			if budget != time.Second {
				options = append(options, WithCompletionTimeout(budget))
			}
			options = append(options, WithSWD(100_000))
			session, err := openSession(t.Context(), peer.device, options...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			type contextKey struct{}
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), contextKey{}, "value"), 100*time.Millisecond)
			defer cancel()
			var deadline time.Time
			waits := 0
			session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: commandSWDSequence, accepted: cancel, check: func(ctx context.Context, _ uint8) error {
				got, ok := ctx.Deadline()
				if !ok || time.Until(got) < budget-100*time.Millisecond || time.Until(got) > budget {
					t.Errorf("completion deadline = %v, budget %v", got, budget)
				}
				if waits == 0 {
					deadline = got
				} else if !got.Equal(deadline) {
					t.Error("packet reset whole-call completion budget")
				}
				if ctx.Value(contextKey{}) != "value" {
					t.Error("completion lost context value")
				}
				waits++
				return nil
			}}
			direction := make([]byte, 25)
			for i := range direction {
				direction[i] = 0xff
			}
			input, err := session.SWDIO(ctx, direction, make([]byte, 25), 200)
			session.claim = peer.device.claim
			if !errors.Is(err, context.Canceled) || len(input) != 25 || session.poisoned || waits < 4 {
				t.Fatalf("SWDIO = %x %v, waits %d, poisoned %t", input, err, waits, session.poisoned)
			}
		})
	}
}

func TestSWDIOIncompleteCommandPoisonsWithoutReplay(t *testing.T) {
	for _, endpoint := range []uint8{1, 0x82} {
		t.Run(map[uint8]string{1: "OUT", 0x82: "IN"}[endpoint], func(t *testing.T) {
			peer := newSWDSequencePeer(64)
			session, err := openSession(t.Context(), peer.device, WithSWD(100_000), WithCompletionTimeout(20*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: commandSWDSequence, accepted: cancel, check: func(ctx context.Context, got uint8) error {
				if got == endpoint {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}}
			_, err = session.SWDIO(ctx, []byte{1}, []byte{1}, 1)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSessionPoisoned) {
				t.Fatalf("SWDIO error = %v", err)
			}
			before := len(peer.commands)
			if _, err := session.SWDIO(t.Context(), []byte{1}, []byte{1}, 1); !errors.Is(err, ErrSessionPoisoned) {
				t.Fatalf("later SWDIO = %v", err)
			}
			if len(peer.commands) != before {
				t.Fatal("poisoned session emitted more traffic")
			}
		})
	}
}

func TestSWDIOPreflightCancellationSendsNothing(t *testing.T) {
	peer := newSWDSequencePeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	before := len(peer.device.claim.operations)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := session.SWDIO(ctx, []byte{1}, []byte{1}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("SWDIO = %v", err)
	}
	if len(peer.device.claim.operations) != before {
		t.Fatal("preflight cancellation submitted USB requests")
	}
}

func TestInvalidCompletionBudgetPrecedesClaim(t *testing.T) {
	for _, budget := range []time.Duration{0, -time.Second} {
		peer := newSWDSequencePeer(64)
		session, err := openSession(t.Context(), peer.device, WithSWD(100_000), WithCompletionTimeout(budget))
		if session != nil || err == nil || len(peer.device.claim.operations) != 0 || len(peer.commands) != 0 {
			t.Fatalf("invalid budget reached claim or traffic: %v %v %v", session, err, peer.device.claim.operations)
		}
	}
}

func TestOpenCleansConfirmedPortAfterCancellation(t *testing.T) {
	peer := newSWDSequencePeer(64)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	peer.device.claim.handle = func(request []byte) ([]byte, error) {
		response, err := peer.handle(request)
		if request[0] == commandSWJClock {
			cancel()
		}
		return response, err
	}
	session, err := openSession(ctx, peer.device, WithSWD(100_000))
	if session != nil || !errors.Is(err, context.Canceled) || peer.device.closes != 1 || peer.device.claim.closes != 1 {
		t.Fatalf("Open = %v %v closes %d/%d", session, err, peer.device.closes, peer.device.claim.closes)
	}
	commands := peer.effectCommands()
	if len(commands) != 3 || commands[2][0] != commandDisconnect {
		t.Fatalf("cleanup commands = %x", commands)
	}
}

func TestSequencePeerRejectsOversizedResponse(t *testing.T) {
	peer := newSWDSequencePeer(16)
	session, err := openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	peer.device.claim.responses[infoFirmwareVersion] = infoResponse(make([]byte, 17))
	_, _, err = session.infoData(t.Context(), infoFirmwareVersion)
	if err == nil || !strings.Contains(err.Error(), "peer: response is 19 bytes, capacity is 16") {
		t.Fatalf("oversized response was truncated: %v", err)
	}
}

type lateResponseClaim struct {
	usbClaim
	late bool
}

func (c *lateResponseClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	transfer, err := c.usbClaim.SubmitBulk(ctx, endpoint, buffer)
	if err == nil && endpoint&0x80 != 0 && !c.late {
		c.late = true
		return lateResponseTransfer{transfer}, nil
	}
	return transfer, err
}

type lateResponseTransfer struct{ usbBulkTransfer }

func (t lateResponseTransfer) Wait(ctx context.Context) (int, error) {
	count, err := t.usbBulkTransfer.Wait(ctx)
	<-ctx.Done()
	return count, err
}

func TestSWDIOBudgetExpiryBetweenPacketsPoisonsPrefix(t *testing.T) {
	peer := newSWDSequencePeer(16)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000), WithCompletionTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	session.claim = &lateResponseClaim{usbClaim: peer.device.claim}
	direction := make([]byte, 25)
	for i := range direction {
		direction[i] = 0xff
	}
	_, err = session.SWDIO(t.Context(), direction, make([]byte, 25), 200)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSessionPoisoned) || len(peer.sequenceCommands) != 1 {
		t.Fatalf("SWDIO = %v, commands %d", err, len(peer.sequenceCommands))
	}
	before := len(peer.commands)
	if _, err := session.SWDIO(t.Context(), []byte{1}, []byte{1}, 1); !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("later SWDIO = %v", err)
	}
	if len(peer.commands) != before {
		t.Fatal("expired stream resumed traffic")
	}
}

func TestSWDIOCancellationPreservesNativeFailure(t *testing.T) {
	peer := newSWDSequencePeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	primary := errors.New("native receive failed")
	peer.device.claim.inputErr = primary
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: commandSWDSequence, accepted: cancel}
	_, err = session.SWDIO(ctx, []byte{1}, []byte{1}, 1)
	if !errors.Is(err, primary) || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("SWDIO = %v", err)
	}
}

func TestSWDIOCancellationBeforeOUTRetainsUsableSession(t *testing.T) {
	peer := newSWDSequencePeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, cancelAfterIN: cancel}
	_, err = session.SWDIO(ctx, []byte{1}, []byte{1}, 1)
	if !errors.Is(err, context.Canceled) || session.poisoned || len(peer.sequenceCommands) != 0 {
		t.Fatalf("SWDIO = %v poisoned %t commands %d", err, session.poisoned, len(peer.sequenceCommands))
	}
	if _, err := session.SWDIO(t.Context(), []byte{1}, []byte{1}, 1); err != nil {
		t.Fatalf("unsent operation damaged session: %v", err)
	}
}

func (t *completionTransfer) Aborted() bool { return t.inner.Aborted() }
