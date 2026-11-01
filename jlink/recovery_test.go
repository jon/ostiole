package jlink

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
	"github.com/jon/ostiole/usb"
)

type borrowedJLink struct {
	*Session
	device *peerUSBDevice
}

func (b *borrowedJLink) SWD(ctx context.Context, config probe.SWDConfig) (probe.Wire, error) {
	session, err := openProbeSession(ctx, b.device, WithSWD(config.MaxClockHz))
	b.Session = session
	if session == nil {
		return nil, err
	}
	return session, err
}
func (b *borrowedJLink) JTAG(ctx context.Context, config probe.JTAGConfig) (probe.JTAGWire, error) {
	session, err := openProbeSession(ctx, b.device, WithJTAG(config.MaxClockHz))
	b.Session = session
	if session == nil {
		return nil, err
	}
	return session, err
}

func borrowedRecoveryWire(t *testing.T, target uint8) (*probe.Probe, probe.Wire, probe.JTAGWire, *peerUSBDevice) {
	t.Helper()
	ops := configuredSWDOperations("another probe", 123)
	ops[5].request = []byte{0xc7, target}
	ops = append(ops,
		peerOperation{request: []byte{0xcf, 0, 1, 0, 0, 0}, response: [][]byte{{0}, {6}}},
		peerOperation{request: []byte{0xc7, target}, response: [][]byte{{target, 0, 0, 0}}},
		ops[6], ops[7],
		peerOperation{request: []byte{0xcf, 0, 2, 0, 0, 0}, response: [][]byte{{0}, {0}}},
	)
	d := metadataPeer(t, ops)
	owner := probe.New(probe.Info{}, &borrowedJLink{device: d})
	var swdWire probe.SWD
	var jtagWire probe.JTAG
	var err error
	if target == 1 {
		swdWire, err = owner.SWD(t.Context(), probe.SWDConfig{MaxClockHz: 123_999})
	} else {
		jtagWire, err = owner.JTAG(t.Context(), probe.JTAGConfig{MaxClockHz: 123_999})
	}
	if err != nil {
		t.Fatal(err)
	}
	return owner, swdWire, jtagWire, d
}

func TestBorrowedJLinkRecoversBeforeNewScan(t *testing.T) {
	for _, target := range []uint8{1, 0} {
		owner, sw, jt, d := borrowedRecoveryWire(t, target)
		transfer := sw.SWDIO
		if target == 0 {
			transfer = jt.JTAGIO
		}
		_, err := transfer(t.Context(), []byte{0}, []byte{0}, 1)
		var status *ScanError
		if !errors.As(err, &status) || status.Status != 6 {
			t.Fatalf("lost status: %v", err)
		}
		if got := sw.MaxTransferBits(); target == 1 && got != 504 {
			t.Fatalf("lost recovery limit: %d", got)
		}
		if got := jt.MaxTransferBits(); target == 0 && got != 504 {
			t.Fatalf("lost recovery limit: %d", got)
		}
		if _, err := transfer(t.Context(), []byte{0}, []byte{0}, 2); err != nil {
			t.Fatalf("cleanup scan could not recover: %v", err)
		}
		if len(d.operations) != 0 || d.writes != 13 {
			t.Fatalf("command order/replay: remaining %d writes %d", len(d.operations), d.writes)
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
		if d.releases != 1 || d.closes != 1 {
			t.Fatal("borrowed owner did not release once")
		}
	}
}

func TestBorrowedJLinkRecoveryRejectsPreflightWithoutTraffic(t *testing.T) {
	for _, target := range []uint8{1, 0} {
		owner, sw, jt, d := borrowedRecoveryWire(t, target)
		transfer := sw.SWDIO
		if target == 0 {
			transfer = jt.JTAGIO
		}
		if _, err := transfer(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
			t.Fatal("missing status")
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		writes := d.writes
		for _, call := range []struct {
			ctx  context.Context
			bits int
			data []byte
		}{
			{nil, 2, []byte{0}}, {ctx, 2, []byte{0}}, {t.Context(), 2, nil}, {t.Context(), 505, make([]byte, 64)},
		} {
			if _, err := transfer(call.ctx, call.data, call.data, call.bits); err == nil {
				t.Fatal("invalid recovery accepted")
			}
		}
		if d.writes != writes {
			t.Fatal("preflight reconfigured probe")
		}
		if _, err := transfer(t.Context(), []byte{0}, []byte{0}, 2); err != nil {
			t.Fatal(err)
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBorrowedJLinkRecoveryKeepsClockAfterRejectedConfiguration(t *testing.T) {
	owner, wire, _, d := borrowedRecoveryWire(t, 1)
	if _, err := wire.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("missing status")
	}
	// A complete malformed clock description keeps the command stream usable.
	d.operations = append(d.operations[:2], append([]peerOperation{d.operations[0], d.operations[1], d.operations[2]}, d.operations[3:]...)...)
	d.operations[1].response = [][]byte{make([]byte, 6)}
	if _, err := wire.SWDIO(t.Context(), []byte{0}, []byte{0}, 2); err == nil {
		t.Fatal("accepted invalid clock description")
	}
	if _, err := wire.SWDIO(t.Context(), []byte{0}, []byte{0}, 2); err != nil {
		t.Fatalf("could not retry configuration: %v", err)
	}
	if len(d.operations) != 0 {
		t.Fatal("configuration/cleanup incomplete")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

type cleanupJLinkDevice struct {
	usbDevice
	pending bool
	err     error
	closes  int
}

func (d *cleanupJLinkDevice) Close() error         { d.closes++; return d.err }
func (d *cleanupJLinkDevice) CleanupPending() bool { return d.pending }

type cleanupJLinkClaim struct {
	usbClaim
	pending bool
	err     error
	closes  int
}

func (c *cleanupJLinkClaim) Close() error         { c.closes++; return c.err }
func (c *cleanupJLinkClaim) CleanupPending() bool { return c.pending }

func TestJLinkRetainsOnlyPendingHostCleanup(t *testing.T) {
	for _, pending := range []bool{false, true} {
		claimErr, deviceErr := errors.New("claim close"), errors.New("device close")
		d := &cleanupJLinkDevice{err: deviceErr}
		claim := &cleanupJLinkClaim{pending: pending, err: claimErr}
		s := &Session{device: d, claim: claim}
		err := s.Close()
		if !errors.Is(err, claimErr) {
			t.Fatalf("lost claim error: %v", err)
		}
		if pending {
			if d.closes != 0 || !s.CleanupPending() {
				t.Fatal("discarded live claim")
			}
			claim.err = nil
			d.pending = true
			if err := s.Close(); !errors.Is(err, deviceErr) || !s.CleanupPending() {
				t.Fatalf("lost live device: %v", err)
			}
			d.pending = false
			if err := s.Close(); !errors.Is(err, deviceErr) {
				t.Fatalf("lost final device error: %v", err)
			}
		} else if !errors.Is(err, deviceErr) {
			t.Fatalf("lost final device error: %v", err)
		}
		closes, releases := d.closes, claim.closes
		if s.CleanupPending() {
			t.Fatal("completed owner retains cleanup")
		}
		if err := s.Close(); err != nil || d.closes != closes || claim.closes != releases {
			t.Fatalf("repeated final close: %v", err)
		}
	}
}

func TestJLinkRestorationQueriesDoNotSendTraffic(t *testing.T) {
	var missing *Session
	if missing.RestorationPossible() || missing.CleanupPending() || new(Session).RestorationPossible() || new(Session).CleanupPending() {
		t.Fatal("empty session reports state")
	}
	owner, wire, _, d := borrowedRecoveryWire(t, 1)
	if _, err := wire.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("missing status")
	}
	writes := d.writes
	if !owner.RestorationPossible() || !owner.CleanupPending() {
		t.Fatal("scan status authorized abandonment")
	}
	if d.writes != writes {
		t.Fatal("query sent traffic")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if owner.RestorationPossible() || owner.CleanupPending() {
		t.Fatal("closed owner reports state")
	}
	for _, poison := range []bool{false, true} {
		s := configuredSession(metadataPeer(t, nil), "probe")
		if poison {
			s.poisoned = true
		} else {
			s.recordLoss(usb.ErrDeviceGone)
		}
		if s.RestorationPossible() || !s.CleanupPending() {
			t.Fatal("terminal session lost state")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type rejectedJLinkClaim struct {
	usbClaim
	err error
}

func (c rejectedJLinkClaim) SubmitBulk(context.Context, uint8, []byte) (usbBulkTransfer, error) {
	return nil, c.err
}

func TestJLinkUnsentFailuresRetainRestoration(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("rejected submission"), usb.ErrDeviceGone} {
		d := metadataPeer(t, nil)
		s := configuredSession(d, "probe")
		s.claim = rejectedJLinkClaim{s.claim, failure}
		err := s.writeExact(t.Context(), []byte{0x05, 100, 0})
		if !errors.Is(err, failure) || errors.Is(err, ErrSessionPoisoned) {
			t.Fatalf("unsent result: %v", err)
		}
		if s.RestorationPossible() == errors.Is(failure, usb.ErrDeviceGone) {
			t.Fatal("wrong restoration classification")
		}
		if d.writes != 0 {
			t.Fatal("rejected submission sent traffic")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type recoverySWDPeer struct {
	t        *testing.T
	device   *peerUSBDevice
	wire     *swdsim.Wire
	fail     bool
	scans    [][]byte
	commands []byte
}

func (p *recoverySWDPeer) write(request []byte) (int, error) {
	p.t.Helper()
	p.commands = append(p.commands, request[0])
	switch request[0] {
	case 0xcf:
		bits := int(binary.LittleEndian.Uint16(request[2:]))
		size := (bits + 7) / 8
		if len(request) != 4+2*size {
			p.t.Fatalf("bad scan length: %x", request)
		}
		p.scans = append(p.scans, append([]byte(nil), request...))
		samples, status := make([]byte, size), byte(6)
		if !p.fail {
			var err error
			samples, err = p.wire.SWDIO(p.t.Context(), request[4:4+size], request[4+size:], bits)
			if err != nil {
				p.t.Fatal(err)
			}
			status = 0
		}
		p.fail = false
		p.device.responses = append(p.device.responses, samples, []byte{status})
	case 0xc7:
		if !bytes.Equal(request, []byte{0xc7, 1}) {
			p.t.Fatalf("wrong recovery interface: %x", request)
		}
		p.device.responses = append(p.device.responses, []byte{1, 0, 0, 0})
	case 0xc0:
		clock := make([]byte, 6)
		binary.LittleEndian.PutUint32(clock, 96_000_000)
		binary.LittleEndian.PutUint16(clock[4:], 24)
		p.device.responses = append(p.device.responses, clock)
	case 0x05:
		if !bytes.Equal(request, []byte{0x05, 123, 0}) {
			p.t.Fatalf("wrong recovery clock: %x", request)
		}
	default:
		p.t.Fatalf("unexpected command: %x", request)
	}
	return len(request), nil
}

func TestBorrowedJLinkSWDReleaseAfterScanStatus(t *testing.T) {
	owner, wire, _, d := borrowedRecoveryWire(t, 1)
	d.operations = nil
	target := dapsim.New(0x2ba01477)
	peer := &recoverySWDPeer{t: t, device: d, wire: swdsim.New(target)}
	d.handleWrite = peer.write
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("connection did not acquire overrun setting")
	}
	before := len(peer.scans)
	peer.fail = true
	_, err := conn.ReadDP(t.Context(), 0)
	var status *ScanError
	if !errors.As(err, &status) || len(peer.scans) != before+1 {
		t.Fatalf("failed scan replayed or lost status: %v", err)
	}
	commands := len(peer.commands)
	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("release could not restore target: %v", err)
	}
	if !bytes.Equal(peer.commands[commands:commands+4], []byte{0xc7, 0xc0, 0x05, 0xcf}) {
		t.Fatal("release did not reconfigure before fresh traffic")
	}
	if bytes.Equal(peer.scans[before], peer.scans[before+1]) {
		t.Fatal("release replayed failed register scan")
	}
	if target.OverrunDetectEnabled() {
		t.Fatal("release did not restore independently observed overrun setting")
	}
	scans := len(peer.scans)
	if err := conn.Release(t.Context()); err != nil || len(peer.scans) != scans {
		t.Fatalf("repeat release sent traffic: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if d.releases != 1 || d.closes != 1 {
		t.Fatal("owner did not release USB once")
	}
}

func TestBorrowedJLinkTerminalFailureStopsRecovery(t *testing.T) {
	for _, loss := range []bool{false, true} {
		owner, wire, _, d := borrowedRecoveryWire(t, 1)
		if _, err := wire.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
			t.Fatal("missing status")
		}
		failure := errors.New("accepted configuration failed")
		d.writeErr = failure
		if loss {
			d.writeErr = errors.Join(failure, usb.ErrDeviceGone)
		}
		if _, err := wire.SWDIO(t.Context(), []byte{0}, []byte{0}, 2); !errors.Is(err, failure) {
			t.Fatalf("lost recovery error: %v", err)
		}
		if owner.RestorationPossible() || !owner.CleanupPending() {
			t.Fatal("terminal recovery lost ownership state")
		}
		writes := d.writes
		if _, err := wire.SWDIO(t.Context(), []byte{0}, []byte{0}, 2); err == nil || d.writes != writes {
			t.Fatalf("terminal recovery sent traffic: %v", err)
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
		if d.releases != 1 || d.closes != 1 {
			t.Fatal("terminal host cleanup incomplete")
		}
	}
}

func TestBorrowedJLinkEmptyJTAGDoesNotReconfigure(t *testing.T) {
	owner, _, wire, d := borrowedRecoveryWire(t, 0)
	if _, err := wire.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("missing status")
	}
	writes := d.writes
	if samples, err := wire.JTAGIO(t.Context(), nil, nil, 0); err != nil || len(samples) != 0 || d.writes != writes {
		t.Fatalf("empty recovery sent traffic: %v", err)
	}
	if _, err := wire.JTAGIO(t.Context(), []byte{0}, []byte{0}, 2); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}
