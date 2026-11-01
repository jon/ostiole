package cmsisdap

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
)

func framingRegisterSession(t *testing.T) (*registerCommandPeer, *Session, *RegisterSWD) {
	t.Helper()
	peer := newRegisterCommandPeer(t, true)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	registers := NewRegisterSWD(session)
	if _, err := registers.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	return peer, session, registers
}

func TestRegisterRawWritesCannotChangeOwnedFraming(t *testing.T) {
	peer, _, registers := framingRegisterSession(t)
	for _, bank := range []uint32{0, 1, 2, 0x01000000} {
		if status, err := registers.WriteDP(t.Context(), 8, bank); status != dap.RegisterCompleted || err != nil {
			t.Fatalf("SELECT=%d,%v", status, err)
		}
		values := []uint32{1}
		if bank == 1 {
			values = []uint32{0x100, 0x200, 0x300}
		}
		for _, value := range values {
			checkRawFramingValue(t, peer, registers, bank, value)
		}
		if bank == 1 {
			if status, err := registers.WriteDP(t.Context(), 4, 1); status != dap.RegisterCompleted || err != nil || peer.target.OverrunDetectEnabled() {
				t.Fatalf("non-framing DLCR bits=%d,%v", status, err)
			}
		}
	}
	if err := registers.Release(t.Context()); err != nil || !peer.target.OverrunDetectEnabled() {
		t.Fatalf("release=%v", err)
	}
}

func checkRawFramingValue(t *testing.T, peer *registerCommandPeer, registers *RegisterSWD, bank, value uint32) {
	t.Helper()
	before := len(peer.commands)
	status, err := registers.WriteDP(t.Context(), 4, value)
	if bank&15 == 2 {
		if status != dap.RegisterCompleted || err != nil || len(peer.commands) != before+1 {
			t.Fatalf("non-framing bank write=%d,%v", status, err)
		}
	} else if status != dap.RegisterUnsent || err == nil || len(peer.commands) != before || peer.target.OverrunDetectEnabled() {
		t.Fatalf("framing bank %#x value %#x=%d,%v; commands=%d/%d", bank, value, status, err, len(peer.commands), before)
	}
}

func TestRegisterUncertainSelectionRequiresKnownBankBeforeWrite(t *testing.T) {
	peer, _, registers := framingRegisterSession(t)
	failed := false
	peer.device.claim.handle = func(command []byte) ([]byte, error) {
		response, err := peer.handle(command)
		if !failed && command[0] == 5 && command[3] == 8 {
			failed = true
			return []byte{5, 1, 4}, err
		}
		return response, err
	}
	status, err := registers.WriteDP(t.Context(), 8, 1)
	if status != dap.RegisterIndeterminate || !errors.Is(err, dap.ErrFault) {
		t.Fatalf("SELECT=%d,%v", status, err)
	}
	before := len(peer.commands)
	if status, err := registers.WriteDP(t.Context(), 4, 0); status != dap.RegisterUnsent || err == nil || len(peer.commands) != before {
		t.Fatalf("unknown-bank write=%d,%v", status, err)
	}
	if status, err := registers.WriteDP(t.Context(), 8, 0); status != dap.RegisterCompleted || err != nil {
		t.Fatalf("explicit bank selection=%d,%v", status, err)
	}
	before = len(peer.commands)
	if status, err := registers.WriteDP(t.Context(), 4, 1); status != dap.RegisterUnsent || err == nil || len(peer.commands) != before {
		t.Fatalf("recovered bank-zero framing=%d,%v", status, err)
	}
	if err := registers.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterConfirmedSelectionKeepsCallerCancellation(t *testing.T) {
	peer, session, registers := framingRegisterSession(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: 5, accepted: cancel}
	status, err := registers.WriteDP(ctx, 8, 1)
	session.claim = peer.device.claim
	if status != dap.RegisterCompleted || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled SELECT=%d,%v", status, err)
	}
	before := len(peer.commands)
	if status, err := registers.WriteDP(t.Context(), 4, 1); status != dap.RegisterCompleted || err != nil || len(peer.commands) != before+1 {
		t.Fatalf("confirmed bank one=%d,%v", status, err)
	}
	before = len(peer.commands)
	if status, err := registers.WriteDP(t.Context(), 4, 0x100); status != dap.RegisterUnsent || err == nil || len(peer.commands) != before {
		t.Fatalf("confirmed bank-one framing=%d,%v", status, err)
	}
	if err := registers.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterOnlyConfirmedDPSelectionChangesFramingBank(t *testing.T) {
	peer, _, registers := framingRegisterSession(t)
	rejected := false
	peer.device.claim.handle = func(command []byte) ([]byte, error) {
		if !rejected && command[0] == 5 && command[3] == 8 {
			rejected = true
			peer.commands = append(peer.commands, append([]byte(nil), command...))
			return []byte{5, 0, 2}, nil
		}
		return peer.handle(command)
	}
	if status, err := registers.WriteDP(t.Context(), 8, 1); status != dap.RegisterRejected || !errors.Is(err, dap.ErrWait) {
		t.Fatalf("rejected SELECT=%d,%v", status, err)
	}
	assertRawOverrunRejected(t, peer, registers)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(peer.commands)
	if status, err := registers.WriteDP(ctx, 8, 1); status != dap.RegisterUnsent || !errors.Is(err, context.Canceled) || len(peer.commands) != before {
		t.Fatalf("unsent SELECT=%d,%v", status, err)
	}
	assertRawOverrunRejected(t, peer, registers)
	if status, err := registers.WriteAP(t.Context(), 8, 1); status != dap.RegisterCompleted || err != nil {
		t.Fatalf("AP write=%d,%v", status, err)
	}
	assertRawOverrunRejected(t, peer, registers)
	if _, status, err := registers.ReadDP(t.Context(), 8); status != dap.RegisterCompleted || err != nil {
		t.Fatalf("RESEND=%d,%v", status, err)
	}
	assertRawOverrunRejected(t, peer, registers)
	if err := registers.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterReconnectEstablishesBankZeroForFraming(t *testing.T) {
	peer, _, registers := framingRegisterSession(t)
	if status, err := registers.WriteDP(t.Context(), 8, 1); status != dap.RegisterCompleted || err != nil {
		t.Fatalf("SELECT=%d,%v", status, err)
	}
	if _, err := registers.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRawOverrunRejected(t, peer, registers)
	if err := registers.Release(t.Context()); err != nil || !peer.target.OverrunDetectEnabled() {
		t.Fatalf("reconnected release=%v", err)
	}
}

func assertRawOverrunRejected(t *testing.T, peer *registerCommandPeer, registers *RegisterSWD) {
	t.Helper()
	before := len(peer.commands)
	if status, err := registers.WriteDP(t.Context(), 4, 1); status != dap.RegisterUnsent || err == nil || len(peer.commands) != before || peer.target.OverrunDetectEnabled() {
		t.Fatalf("raw ORUNDETECT write=%d,%v", status, err)
	}
}
