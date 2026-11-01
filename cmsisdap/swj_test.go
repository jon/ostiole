package cmsisdap

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestSWJSequenceUsesMandatoryCommandWithExactBits(t *testing.T) {
	for _, bits := range []int{1, 8, 136, 256} {
		peer := newSWDCommandPeer(64)
		peer.device.claim.responses[infoProtocolVersion] = infoResponse([]byte("0254\x00"))
		session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		var commands [][]byte
		peer.device.claim.handle = func(command []byte) ([]byte, error) {
			if command[0] != 0x12 {
				return peer.handle(command)
			}
			commands = append(commands, append([]byte(nil), command...))
			return []byte{0x12, 0}, nil
		}
		output := bytes.Repeat([]byte{0xa5}, (bits+7)/8)
		if err := session.SWJSequence(t.Context(), output, bits); err != nil {
			t.Fatal(err)
		}
		want := append([]byte{0x12, byte(bits)}, output...)
		if bits == 1 {
			want[2] = 1
		}
		if len(commands) != 1 || !bytes.Equal(commands[0], want) {
			t.Fatalf("%d-bit command = %x, want %x", bits, commands, want)
		}
	}
}

func TestSWJSequenceRejectsInvalidCallsBeforeTraffic(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(peer.commands)
	for _, tc := range []struct {
		ctx    context.Context
		output []byte
		bits   int
	}{
		{t.Context(), []byte{1}, 0},
		{t.Context(), []byte{1}, -1},
		{t.Context(), make([]byte, 33), 257},
		{t.Context(), nil, 1},
		{t.Context(), []byte{1}, 9},
		{nil, []byte{1}, 1},
		{ctx, []byte{1}, 1},
	} {
		if err := session.SWJSequence(tc.ctx, tc.output, tc.bits); err == nil || len(peer.commands) != before {
			t.Fatalf("invalid %d-bit call sent traffic or succeeded: %v", tc.bits, err)
		}
	}
	session.packetSize = 4
	if err := session.SWJSequence(t.Context(), []byte{1, 2, 3}, 24); err == nil || len(peer.commands) != before {
		t.Fatal("oversized command sent traffic")
	}
}

func TestSWJSequenceFinishesAcceptedCommandAfterCancellation(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	commands := 0
	peer.device.claim.handle = func(command []byte) ([]byte, error) {
		if command[0] != 0x12 {
			return peer.handle(command)
		}
		commands++
		return []byte{0x12, 0}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session.claim = &acceptanceClaim{peerUSBClaim: peer.device.claim, command: 0x12, accepted: cancel}
	if err := session.SWJSequence(ctx, []byte{0xff}, 8); !errors.Is(err, context.Canceled) || session.poisoned || commands != 1 {
		t.Fatalf("accepted sequence = %v, poisoned %t, commands %d", err, session.poisoned, commands)
	}
}

func TestSWJEntryAdapterRejectsCaptureBeforeTraffic(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	before := len(peer.commands)
	wire := swjOutput{session: session}
	if _, err := wire.SWDIO(t.Context(), []byte{0xfe}, []byte{0xff}, 8); err == nil || len(peer.commands) != before {
		t.Fatal("entry adapter sent an input phase through an output-only command")
	}
}
