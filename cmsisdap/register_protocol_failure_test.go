package cmsisdap

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestCMSISTargetProtocolFailureRetainsHostRestoration(t *testing.T) {
	for _, status := range []byte{0, 3, 5, 6, 7, 8} {
		t.Run(fmt.Sprintf("%#x", status), func(t *testing.T) { exerciseCMSISTargetProtocolFailure(t, status) })
	}
}

func exerciseCMSISTargetProtocolFailure(t *testing.T, status byte) {
	t.Helper()
	peer := newRegisterCommandPeer(t, true)
	owner, backend := connectCommandProbe(t, peer)
	memory, err := owner.OpenMemAP(t.Context(), dap.NewAPSel(1))
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	peer.device.claim.handle = func(command []byte) ([]byte, error) {
		if command[0] == 5 && !failed {
			failed = true
			peer.commands = append(peer.commands, append([]byte(nil), command...))
			return []byte{5, 0, status}, nil
		}
		return peer.handle(command)
	}
	_, err = memory.ReadWord(t.Context(), 0x100)
	if !errors.Is(err, dap.ErrProtocol) || !errors.Is(err, dap.ErrIndeterminate) || errors.Is(err, ErrSessionPoisoned) || !backend.RestorationPossible() {
		t.Fatalf("target status %#x=%v, possible=%t", status, err, backend.RestorationPossible())
	}
	before := len(peer.commands)
	if err := owner.Close(); err != nil || len(peer.commands) == before || !peer.target.OverrunDetectEnabled() || peer.device.closes != 1 {
		t.Fatalf("target status %#x cleanup=%v", status, err)
	}
	if err := owner.Close(); err != nil || peer.device.closes != 1 {
		t.Fatalf("repeated close=%v", err)
	}
}
