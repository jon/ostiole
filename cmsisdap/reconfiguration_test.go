package cmsisdap

import (
	"reflect"
	"testing"
)

func TestReconfigurationDisconnectsBeforeReconnect(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	before := len(peer.effectCommands())
	if err := session.ConfigureSWD(t.Context(), 200_000); err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{commandDisconnect}, {commandConnect, portSWD}, {commandSWJClock, 0x40, 0x0d, 0x03, 0}}
	if got := peer.effectCommands()[before:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("reconfiguration commands = %x, want %x", got, want)
	}
	peer.disconnectStatuses = []byte{statusError}
	before = len(peer.effectCommands())
	if err := session.ConfigureSWD(t.Context(), 300_000); err == nil {
		t.Fatal("failed disconnect permitted reconfiguration")
	}
	if session.MaxClockHz() != 200_000 || !session.RestorationPossible() || !session.configured || !session.connected {
		t.Fatal("failed disconnect lost the old configuration")
	}
	if got := peer.effectCommands()[before:]; !reflect.DeepEqual(got, [][]byte{{commandDisconnect}}) {
		t.Fatalf("commands after failed disconnect=%x", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedReconnectLeavesNoInheritedPortObligation(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if err != nil {
		t.Fatal(err)
	}
	peer.connectPort = 0
	before := len(peer.effectCommands())
	if err := session.ConfigureSWD(t.Context(), 200_000); err == nil {
		t.Fatal("failed reconnect accepted")
	}
	if got := peer.effectCommands()[before:]; !reflect.DeepEqual(got, [][]byte{{commandDisconnect}, {commandConnect, portSWD}}) {
		t.Fatalf("reconnect commands = %x", got)
	}
	before = len(peer.effectCommands())
	if err := session.Close(); err != nil || len(peer.effectCommands()) != before {
		t.Fatalf("reconnect cleanup replayed a command: %v", err)
	}
}

func TestUnconfiguredSessionRejectsSWDWithoutTraffic(t *testing.T) {
	for _, failedConfiguration := range []bool{false, true} {
		peer := newSWDSequencePeer(64)
		session, err := openSession(t.Context(), peer.device)
		if err != nil {
			t.Fatal(err)
		}
		if failedConfiguration {
			peer.clockStatus = statusError
			peer.disconnectStatuses = []byte{statusError}
			if err := session.ConfigureSWD(t.Context(), 100_000); err == nil || !session.connected || session.configured {
				t.Fatalf("failed configuration state = %v", err)
			}
		}
		before := len(peer.commands)
		if _, err := session.SWDIO(t.Context(), []byte{1}, []byte{1}, 1); err == nil {
			t.Fatal("unconfigured SWDIO accepted")
		}
		if len(peer.commands) != before || len(peer.sequenceCommands) != 0 {
			t.Fatal("unconfigured SWDIO sent target traffic")
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
