package cmsisdap

import (
	"strings"
	"testing"
)

func TestSWDIOAcceptsSupportedNumericVersions(t *testing.T) {
	for _, version := range []string{"1.2", "1.2.0", "1.10", "2.0", "2.1", "2.1.0"} {
		t.Run(version, func(t *testing.T) {
			peer := newSWDSequencePeer(64)
			peer.device.claim.responses[infoProtocolVersion] = infoResponse([]byte(version + "\x00"))
			session, err := openSession(t.Context(), peer.device)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := session.Close(); err != nil {
					t.Error(err)
				}
			})
			if len(peer.effectCommands()) != 0 {
				t.Fatal("metadata open activated the target port")
			}
			if err := session.ConfigureSWD(t.Context(), 1_000_000); err != nil {
				t.Fatalf("ConfigureSWD protocol %q: %v", version, err)
			}
			if session.MaxClockHz() != 1_000_000 || len(peer.effectCommands()) != 2 {
				t.Fatalf("configured state = clock %d, commands %x", session.MaxClockHz(), peer.effectCommands())
			}
			if _, err := session.SWDIO(t.Context(), []byte{0xff}, []byte{0}, 8); err != nil {
				t.Fatalf("SWDIO protocol %q: %v", version, err)
			}
			if len(peer.sequenceCommands) != 1 {
				t.Fatal("supported sequence did not execute exactly once")
			}
		})
	}
}

func TestSWDIORejectsUnprovenSequenceVersionsBeforeEffects(t *testing.T) {
	for _, version := range []string{
		"1.1", "1.1.99", "0.9.0", "0254", "0255", "unknown", "2", "2.1beta",
		"2.1.0-extra", "2.1.0.1", " 2.1.0", "+2.1.0", "2.-1.0", "2.1.-1", "2..0",
		"999999999999999999999999999.0", "2.1.999999999999999999999999999",
	} {
		t.Run(version, func(t *testing.T) {
			peer := newSWDSequencePeer(64)
			peer.device.claim.responses[infoProtocolVersion] = infoResponse([]byte(version + "\x00"))
			session, err := openSession(t.Context(), peer.device)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := session.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := session.ConfigureSWD(t.Context(), 1_000_000); err != nil {
				t.Fatal(err)
			}
			before := len(peer.commands)
			_, err = session.SWDIO(t.Context(), []byte{0xff}, []byte{0}, 8)
			if err == nil || !strings.Contains(err.Error(), "does not support DAP_SWD_Sequence") {
				t.Fatalf("SWDIO protocol %q error = %v", version, err)
			}
			if len(peer.commands) != before || session.MaxClockHz() != 1_000_000 {
				t.Fatalf("unproven sequence version sent traffic: %x", peer.effectCommands())
			}
		})
	}
}
