package cmsisdap

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestRegisterPreflightRejectsInvalidOwnersWithoutTraffic(t *testing.T) {
	var zero RegisterSWD
	var absent context.Context
	for _, registers := range []*RegisterSWD{nil, &zero, NewRegisterSWD(nil)} {
		if _, err := registers.Connect(t.Context()); err == nil || registers.CleanupPending() {
			t.Fatalf("invalid owner acquired protocol state: %v", err)
		}
		if _, status, err := registers.ReadDP(t.Context(), 0); status != dap.RegisterUnsent || err == nil {
			t.Fatalf("invalid owner read = %d,%v", status, err)
		}
		if err := registers.Release(absent); err != nil {
			t.Fatal(err)
		}
	}
	peer := newRegisterCommandPeer(t, false)
	session, err := openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	before := len(peer.commands)
	registers := NewRegisterSWD(session)
	if _, err := registers.Connect(t.Context()); err == nil || len(peer.commands) != before {
		t.Fatal("unconfigured entry sent traffic")
	}
	if err := session.ConfigureSWD(t.Context(), 1_000_000); err != nil {
		t.Fatal(err)
	}
	before = len(peer.commands)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if _, err := registers.Connect(ctx); err == nil || len(peer.commands) != before {
			t.Fatal("invalid-context entry sent traffic")
		}
	}
	session.packetSize = 18
	if _, err := registers.Connect(t.Context()); err == nil || len(peer.commands) != before {
		t.Fatal("undersized entry sent traffic")
	}
}

func TestRegisterActivePreflightAndReleasedCallsSendNothing(t *testing.T) {
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
	before := len(peer.commands)
	for _, addr := range []byte{1, 2, 3, 16, 255} {
		if _, status, err := registers.ReadDP(t.Context(), addr); status != dap.RegisterUnsent || err == nil {
			t.Fatalf("invalid read offset = %d,%v", status, err)
		}
		if status, err := registers.WriteAP(t.Context(), addr, 42); status != dap.RegisterUnsent || err == nil {
			t.Fatalf("invalid write offset = %d,%v", status, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := registers.Release(ctx); !errors.Is(err, context.Canceled) || !registers.CleanupPending() || len(peer.commands) != before {
		t.Fatalf("canceled release = %v", err)
	}
	if err := registers.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	before = len(peer.commands)
	if _, status, err := registers.ReadAP(t.Context(), 12); status != dap.RegisterUnsent || err == nil || len(peer.commands) != before {
		t.Fatalf("released read = %d,%v", status, err)
	}
}

func TestRegisterLiveReleaseFailureRetainsOwnerForRetry(t *testing.T) {
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
	failed := false
	peer.device.claim.handle = func(command []byte) ([]byte, error) {
		if command[0] == 4 && !failed {
			failed = true
			return []byte{4, 0xff}, nil
		}
		return peer.handle(command)
	}
	if err := registers.Release(t.Context()); err == nil || !registers.CleanupPending() || !session.RestorationPossible() {
		t.Fatalf("live release failure = %v", err)
	}
	if err := registers.Release(t.Context()); err != nil || registers.CleanupPending() || !peer.target.OverrunDetectEnabled() {
		t.Fatalf("retried release = %v", err)
	}
}
