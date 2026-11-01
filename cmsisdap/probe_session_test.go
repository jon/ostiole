package cmsisdap

import (
	"errors"
	"strings"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

func TestRegisterProbeFailureLeavesAttachmentWithCaller(t *testing.T) {
	for _, stage := range []string{"option", "configuration", "claim", "setup"} {
		t.Run(stage, func(t *testing.T) {
			peer := newSWDCommandPeer(64)
			config := probe.SWDConfig{MaxClockHz: 100_000}
			primary := errors.New("open failed")
			switch stage {
			case "option":
				config.MaxClockHz = 0
			case "configuration":
				peer.device.configErr = primary
			case "claim":
				peer.device.claimErr = primary
			case "setup":
				peer.clockStatus = statusError
			}
			session, err := openRegisterProbeSession(t.Context(), peer.device, config)
			if session != nil || err == nil {
				t.Fatalf("activation = (%T,%v)", session, err)
			}
			if peer.device.closes != 0 {
				t.Fatalf("borrowed attachment closed %d times", peer.device.closes)
			}
			if stage == "configuration" || stage == "claim" {
				if !errors.Is(err, primary) {
					t.Fatalf("lost operation: %v", err)
				}
			}
			if stage == "setup" && peer.device.claim.closes != 1 {
				t.Fatal("completed claim cleanup lost")
			}
			final := errors.New("final host close")
			peer.device.closeErrs = []error{final}
			if err := peer.device.Close(); !errors.Is(err, final) || peer.device.CleanupPending() {
				t.Fatalf("caller cleanup=%v", err)
			}
		})
	}
}

func TestRegisterProbeSuccessTransfersHostWithoutProtocolEntry(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openRegisterProbeSession(t.Context(), peer.device, probe.SWDConfig{MaxClockHz: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	before := len(peer.commands)
	registers := session.Registers()
	if registers == nil || registers.CleanupPending() || !session.(probe.CleanupState).CleanupPending() || len(peer.commands) != before {
		t.Fatal("borrow confused protocol and host state")
	}
	for _, command := range peer.effectCommands() {
		if command[0] != commandConnect && command[0] != commandSWJClock {
			t.Fatalf("activation entered protocol: %x", command)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil || peer.device.closes != 1 || peer.device.claim.closes != 1 {
		t.Fatalf("host close replayed: %v", err)
	}
}

func TestRegisterProbeFailureTransfersPendingCleanup(t *testing.T) {
	for _, stage := range []string{"claim", "disconnect"} {
		t.Run(stage, func(t *testing.T) {
			peer := newSWDCommandPeer(64)
			cleanup := errors.New("live claim close failed")
			if stage == "claim" {
				peer.device.claim.alternateErr = errors.New("alternate failed")
				peer.device.claim.closeErrs = []error{cleanup}
			} else {
				peer.clockStatus = statusError
				peer.disconnectStatuses = []byte{statusError, statusError, statusOK}
			}
			session, err := openRegisterProbeSession(t.Context(), peer.device, probe.SWDConfig{MaxClockHz: 100_000})
			if session == nil || err == nil || !session.(probe.CleanupState).CleanupPending() || peer.device.closes != 0 {
				t.Fatalf("pending activation=(%T,%v)", session, err)
			}
			if stage == "claim" && !errors.Is(err, cleanup) {
				t.Fatalf("lost cleanup: %v", err)
			}
			if stage == "disconnect" && !strings.Contains(err.Error(), "DAP_Disconnect") {
				t.Fatalf("lost disconnect: %v", err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			if err := session.Close(); err != nil || peer.device.closes != 1 {
				t.Fatalf("pending ownership lost or replayed: %v", err)
			}
		})
	}
}

func TestRegisterProbeTransferredFinalHostErrorIsNotReplayed(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openRegisterProbeSession(t.Context(), peer.device, probe.SWDConfig{MaxClockHz: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	final := errors.New("final raw USB close failed")
	peer.device.closeErrs = []error{final}
	if err := session.Close(); !errors.Is(err, final) || session.(probe.CleanupState).CleanupPending() {
		t.Fatalf("final close=%v", err)
	}
	if err := session.Close(); err != nil || peer.device.closes != 1 {
		t.Fatalf("final error replayed: %v", err)
	}
}

func TestProbeDeviceReportsOnlyTransferredHostOwnership(t *testing.T) {
	device := metadataPeer()
	borrowed := &probeDevice{usbDevice: device}
	if borrowed.CleanupPending() {
		t.Fatal("untransferred borrow owns attachment")
	}
	if err := borrowed.Close(); err != nil || device.closes != 0 {
		t.Fatal("untransferred borrow closed attachment")
	}
	borrowed.owned = true
	if !borrowed.CleanupPending() || device.closes != 0 {
		t.Fatal("transferred query lost host ownership or performed cleanup")
	}
	if err := borrowed.Close(); err != nil || borrowed.CleanupPending() || device.closes != 1 {
		t.Fatalf("host release=%v", err)
	}
}

func TestRegisterProbeSessionRestoresThroughDAPOwner(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		peer := newRegisterCommandPeer(t, inherited)
		session, err := openRegisterProbeSession(t.Context(), peer.device, probe.SWDConfig{MaxClockHz: 1_000_000})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		dp := dap.NewDebugPort(dap.RegisterDP(session.Registers()))
		if _, err := dp.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		mem, err := dap.OpenMemAP(t.Context(), dp, dap.NewAPSel(1))
		if err != nil {
			t.Fatal(err)
		}
		if err := mem.WriteWord(t.Context(), 0x100, 0x12345678); err != nil {
			t.Fatal(err)
		}
		if value, err := mem.ReadWord(t.Context(), 0x100); value != 0x12345678 || err != nil {
			t.Fatalf("memory=%#x,%v", value, err)
		}
		if err := mem.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := dp.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if session.Registers().CleanupPending() || !session.(probe.CleanupState).CleanupPending() || peer.target.OverrunDetectEnabled() != inherited {
			t.Fatal("protocol cleanup lost inherited state or host owner")
		}
		if err := session.Close(); err != nil || peer.device.closes != 1 || peer.device.claim.closes != 1 {
			t.Fatalf("host cleanup=%v", err)
		}
	}
}

func TestRegisterProbeLossKeepsExactCleanupOwner(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "released claim", true: "pending claim"}[pending], func(t *testing.T) {
			peer := newSWDCommandPeer(64)
			peer.device.claim.handle = func(command []byte) ([]byte, error) {
				if command[0] == commandSWJClock {
					return nil, usb.ErrDeviceGone
				}
				return peer.handle(command)
			}
			cleanup := errors.New("pending claim cleanup")
			if pending {
				peer.device.claim.closeErrs = []error{cleanup}
			}
			session, err := openRegisterProbeSession(t.Context(), peer.device, probe.SWDConfig{MaxClockHz: 1_000_000})
			if !errors.Is(err, usb.ErrDeviceGone) || peer.device.closes != 0 || peer.device.claim.closes != 1 {
				t.Fatalf("loss=%v, closes=%d/%d", err, peer.device.claim.closes, peer.device.closes)
			}
			if !pending {
				if session != nil {
					t.Fatal("retained session after completed claim cleanup")
				}
				if err := peer.device.Close(); err != nil || peer.device.closes != 1 {
					t.Fatalf("caller cleanup=%v", err)
				}
				return
			}
			if session == nil || !errors.Is(err, cleanup) {
				t.Fatalf("lost retained claim: %v", err)
			}
			finishTerminalProbeCleanup(t, session, peer)
		})
	}
}

func finishTerminalProbeCleanup(t *testing.T, session probeusb.RegisterSession, peer *swdCommandPeer) {
	t.Helper()
	if session.(probe.RestorationState).RestorationPossible() || !session.(probe.CleanupState).CleanupPending() {
		t.Fatal("terminal session lost pending host owner")
	}
	before := len(peer.commands)
	if err := session.Close(); !errors.Is(err, usb.ErrDeviceGone) || peer.device.closes != 1 || peer.device.claim.closes != 2 || len(peer.commands) != before {
		t.Fatalf("terminal cleanup=%v", err)
	}
	if err := session.Close(); err != nil || peer.device.closes != 1 {
		t.Fatalf("terminal cleanup replayed=%v", err)
	}
}
