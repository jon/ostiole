package cmsisdap

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestRegisterCommandEncodesOneCompletedOperation(t *testing.T) {
	for _, request := range []byte{0, 2, 4, 6, 8, 10, 12, 14, 1, 3, 5, 7, 9, 11, 13, 15} {
		peer := newSWDCommandPeer(64)
		session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		var commands [][]byte
		peer.device.claim.handle = func(command []byte) ([]byte, error) {
			if command[0] != 5 {
				return peer.handle(command)
			}
			commands = append(commands, append([]byte(nil), command...))
			response := []byte{5, 1, 1}
			if request&2 != 0 {
				response = binary.LittleEndian.AppendUint32(response, 0x12345678)
			}
			return response, nil
		}
		value, status, err := session.transferRegister(t.Context(), request, 0x87654321)
		if err != nil || status != dap.RegisterCompleted {
			t.Fatalf("request %#x = %#x,%d,%v", request, value, status, err)
		}
		want := []byte{5, 0, 1, request}
		if request&2 == 0 {
			want = binary.LittleEndian.AppendUint32(want, 0x87654321)
		} else if value != 0x12345678 {
			t.Fatalf("completed value = %#x", value)
		}
		if !reflect.DeepEqual(commands, [][]byte{want}) {
			t.Fatalf("request %#x sent %x, want one %x", request, commands, want)
		}
	}
}

func TestRegisterCommandKeepsRejectionAndCompletionDistinct(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response []byte
		status   dap.RegisterStatus
		cause    error
		poisoned bool
	}{
		{"WAIT before acceptance", []byte{5, 0, 2}, dap.RegisterRejected, dap.ErrWait, false},
		{"FAULT before acceptance", []byte{5, 0, 4}, dap.RegisterRejected, dap.ErrFault, false},
		{"WAIT after acceptance", []byte{5, 1, 2}, dap.RegisterIndeterminate, dap.ErrWait, false},
		{"FAULT after acceptance", []byte{5, 1, 4}, dap.RegisterCompleted, dap.ErrFault, false},
		{"protocol failure", []byte{5, 0, 8}, dap.RegisterIndeterminate, dap.ErrProtocol, false},
		{"no target", []byte{5, 0, 7}, dap.RegisterIndeterminate, dap.ErrProtocol, false},
		{"unexpected count", []byte{5, 2, 1}, dap.RegisterIndeterminate, dap.ErrProtocol, true},
		{"empty completion", []byte{5, 0, 1}, dap.RegisterIndeterminate, dap.ErrProtocol, true},
		{"short read", []byte{5, 1, 1, 0x42}, dap.RegisterIndeterminate, dap.ErrProtocol, true},
		{"short header", []byte{5, 1}, dap.RegisterIndeterminate, dap.ErrProtocol, true},
		{"wrong command", []byte{6, 1, 1}, dap.RegisterIndeterminate, dap.ErrProtocol, true},
		{"unimplemented", []byte{0xff}, dap.RegisterUnsent, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newSWDCommandPeer(64)
			session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			attempts := 0
			peer.device.claim.handle = func(command []byte) ([]byte, error) {
				if command[0] != 5 {
					return peer.handle(command)
				}
				attempts++
				return tc.response, nil
			}
			value, status, err := session.transferRegister(t.Context(), 3, 0)
			if value != 0 || status != tc.status || err == nil || tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("outcome = %#x,%d,%v", value, status, err)
			}
			if session.poisoned != tc.poisoned || attempts != 1 {
				t.Fatalf("poisoned %t, attempts %d", session.poisoned, attempts)
			}
		})
	}
}

func TestRegisterCommandCancellationKeepsPhysicalOutcome(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		peer := newSWDCommandPeer(64)
		session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		commands := 0
		peer.device.claim.handle = func(command []byte) ([]byte, error) {
			if command[0] != 5 {
				return peer.handle(command)
			}
			commands++
			return []byte{5, 1, 1, 42, 0, 0, 0}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		claim := &acceptanceClaim{peerUSBClaim: peer.device.claim, command: 5}
		if accepted {
			claim.accepted = cancel
		} else {
			claim.cancelAfterIN = cancel
		}
		session.claim = claim
		value, status, err := session.transferRegister(ctx, 3, 0)
		cancel()
		if !errors.Is(err, context.Canceled) || session.poisoned {
			t.Fatalf("canceled transfer = %#x,%d,%v", value, status, err)
		}
		if accepted {
			if value != 42 || status != dap.RegisterCompleted || commands != 1 {
				t.Fatalf("lost completed read: %#x,%d,commands %d", value, status, commands)
			}
		} else if value != 0 || status != dap.RegisterUnsent || commands != 0 {
			t.Fatalf("unsent read = %#x,%d,commands %d", value, status, commands)
		}
	}
}

func TestRegisterCommandLostCompletionIsNeverReplayed(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	commands := 0
	peer.device.claim.handle = func(command []byte) ([]byte, error) {
		if command[0] != 5 {
			return peer.handle(command)
		}
		commands++
		return []byte{5, 1, 1}, nil
	}
	native := errors.New("response completion failed")
	peer.device.claim.inputErr = native
	_, status, err := session.transferRegister(t.Context(), 1, 42)
	if status != dap.RegisterIndeterminate || !errors.Is(err, native) || !errors.Is(err, dap.ErrIndeterminate) || !session.poisoned {
		t.Fatalf("ambiguous write = %d,%v", status, err)
	}
	_, status, err = session.transferRegister(t.Context(), 1, 42)
	if status != dap.RegisterUnsent || !errors.Is(err, ErrSessionPoisoned) || commands != 1 {
		t.Fatalf("poisoned write = %d,%v,commands %d", status, err, commands)
	}
}

func TestRegisterDPReadRejectsImpossibleCompletionStatus(t *testing.T) {
	for _, ack := range []byte{2, 4} {
		peer := newSWDCommandPeer(64)
		session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		peer.device.claim.handle = func(command []byte) ([]byte, error) {
			if command[0] != 5 {
				return peer.handle(command)
			}
			return []byte{5, 1, ack}, nil
		}
		_, status, err := session.transferRegister(t.Context(), 2, 0)
		if status != dap.RegisterIndeterminate || !errors.Is(err, dap.ErrProtocol) || !session.poisoned {
			t.Fatalf("impossible DP read = %d,%v", status, err)
		}
	}
}
