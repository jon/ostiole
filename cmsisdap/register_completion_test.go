package cmsisdap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestRegisterCompletionUsesIndependentBudget(t *testing.T) {
	for _, budget := range []time.Duration{time.Second, 2 * time.Second} {
		t.Run(budget.String(), func(t *testing.T) {
			peer := newRegisterCommandPeer(t, false)
			session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000), WithCompletionTimeout(budget))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			registers := NewRegisterSWD(session)
			if _, err := registers.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			type key struct{}
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), key{}, "register operation"), 100*time.Millisecond)
			defer cancel()
			waits := 0
			var deadline time.Time
			session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: 5, accepted: cancel, check: func(completion context.Context, _ uint8) error {
				got, ok := completion.Deadline()
				if !ok || time.Until(got) < budget-100*time.Millisecond || time.Until(got) > budget {
					t.Errorf("completion deadline=%v, budget=%v", got, budget)
				}
				if waits == 0 {
					deadline = got
				} else if !got.Equal(deadline) {
					t.Error("register command renewed completion budget")
				}
				if completion.Value(key{}) != "register operation" {
					t.Error("completion lost caller values")
				}
				waits++
				return nil
			}}
			value, status, err := registers.ReadDP(ctx, 0)
			session.claim = peer.device.claim
			if value != 0x2ba01477 || status != dap.RegisterCompleted || !errors.Is(err, context.Canceled) || waits != 2 || session.poisoned {
				t.Fatalf("read=%#x,%d,%v; waits=%d", value, status, err, waits)
			}
			if err := registers.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRegisterCompletionExpiryPoisonsWithoutReplay(t *testing.T) {
	for _, endpoint := range []uint8{1, 0x82} {
		peer := newRegisterCommandPeer(t, false)
		session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000), WithCompletionTimeout(20*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		registers := NewRegisterSWD(session)
		if _, err := registers.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: 5, accepted: cancel, check: func(completion context.Context, got uint8) error {
			if got == endpoint {
				<-completion.Done()
				return completion.Err()
			}
			return nil
		}}
		_, status, err := registers.ReadDP(ctx, 0)
		session.claim = peer.device.claim
		if status != dap.RegisterIndeterminate || !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSessionPoisoned) {
			t.Fatalf("expired read=%d,%v", status, err)
		}
		before := len(peer.commands)
		if _, status, err := registers.ReadDP(t.Context(), 0); status != dap.RegisterUnsent || err == nil {
			t.Fatalf("poisoned read=%d,%v", status, err)
		}
		if err := registers.Release(t.Context()); !errors.Is(err, ErrSessionPoisoned) || !registers.CleanupPending() || len(peer.commands) != before {
			t.Fatalf("poisoned release=%v", err)
		}
	}
}

func TestRegisterReleaseRejectsChangedIdentityBeforeRestoration(t *testing.T) {
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
	originalTarget, originalWire := peer.target, peer.wire
	peer.target = dapsim.New(0x0bb11477)
	peer.wire = swdsim.New(peer.target)
	before := len(peer.commands)
	if err := registers.Release(t.Context()); !errors.Is(err, dap.ErrProtocol) || !registers.CleanupPending() {
		t.Fatalf("changed identity release=%v", err)
	}
	reads := 0
	for _, command := range peer.commands[before:] {
		if command[0] == 5 {
			reads++
			if len(command) != 4 || command[3] != 2 {
				t.Fatalf("changed target received restoration: %x", command)
			}
		}
	}
	if reads != 1 || peer.target.OverrunDetectEnabled() {
		t.Fatal("changed target was mutated or identity not checked")
	}
	peer.target, peer.wire = originalTarget, originalWire
	if err := registers.Release(t.Context()); err != nil || registers.CleanupPending() || !peer.target.OverrunDetectEnabled() {
		t.Fatalf("original restoration=%v", err)
	}
}
