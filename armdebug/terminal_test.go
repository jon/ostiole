package armdebug_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/probe"
)

type terminalBench struct {
	*bench
	possible   bool
	pending    bool
	retainHost bool
}

func (b *terminalBench) SWD(context.Context, probe.SWDConfig) (probe.Wire, error) { return b, nil }
func (b *terminalBench) RestorationPossible() bool                                { return b.possible }
func (b *terminalBench) CleanupPending() bool                                     { return b.pending }
func (b *terminalBench) Close() error {
	err := b.bench.Close()
	b.pending = b.retainHost
	return err
}

func openTerminalBench(t *testing.T) (*armdebug.Conn, *terminalBench) {
	t.Helper()
	b := &terminalBench{bench: newBench(), possible: true, pending: true}
	if err := b.target.AddMEMAP(dap.NewAPSel(0), 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.OpenMemAP(t.Context(), dap.NewAPSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	return c, b
}

func TestTerminalCloseStopsRestorationAndReleasesHost(t *testing.T) {
	for _, cause := range []string{"unplugged", "poisoned"} {
		t.Run(cause, func(t *testing.T) {
			c, b := openTerminalBench(t)
			b.possible = false
			b.wireErr = errors.New(cause)
			before := b.transfers
			err := c.Close()
			if !errors.Is(err, armdebug.ErrRestorationAbandoned) {
				t.Errorf("missing abandoned restoration: %v", err)
			}
			if b.closes != 1 || b.transfers != before {
				t.Errorf("terminal close: host closes %d, wire calls %d -> %d", b.closes, before, b.transfers)
			}
			if err := c.Close(); err != nil || b.closes != 1 || b.transfers != before {
				t.Fatalf("repeat close: %v", err)
			}
		})
	}
}

func TestTerminalCloseRetainsOnlyPendingHostCleanup(t *testing.T) {
	c, b := openTerminalBench(t)
	b.possible = false
	b.wireErr = errors.New("poisoned")
	hostErr := errors.New("host still pending")
	b.closeErr = hostErr
	b.retainHost = true
	before := b.transfers
	if err := c.Close(); !errors.Is(err, hostErr) || b.closes != 1 {
		t.Errorf("first close: %v", err)
	}
	b.retainHost = false
	if err := c.Close(); !errors.Is(err, hostErr) || b.closes != 2 {
		t.Errorf("final host error: %v", err)
	}
	if err := c.Close(); err != nil || b.closes != 2 || b.transfers != before {
		t.Errorf("released owner close: %v, host calls %d, wire calls %d -> %d", err, b.closes, before, b.transfers)
	}
}

func TestFinalHostCloseErrorIsReturnedOnlyOnce(t *testing.T) {
	c, b := openTerminalBench(t)
	hostErr := errors.New("cached final close error")
	b.closeErr = hostErr
	if err := c.Close(); !errors.Is(err, hostErr) {
		t.Fatal(err)
	}
	before := b.transfers
	if err := c.Close(); err != nil || b.closes != 1 || b.transfers != before {
		t.Fatalf("final error repeated: %v, host closes %d", err, b.closes)
	}
}

func TestTerminalFailureDuringRestorationPreservesBothErrors(t *testing.T) {
	c, b := openTerminalBench(t)
	restoreErr := errors.New("restoration lost wire")
	hostErr := errors.New("final host error")
	b.closeErr = hostErr
	b.beforeTransfer = func(context.Context) { b.possible = false; b.wireErr = restoreErr }
	before := b.transfers
	err := c.Close()
	if !errors.Is(err, restoreErr) || !errors.Is(err, hostErr) || !errors.Is(err, armdebug.ErrRestorationAbandoned) {
		t.Fatalf("lost restoration/abandonment/host error: %v", err)
	}
	if b.closes != 1 || b.transfers != before+1 {
		t.Fatalf("continued terminal restoration: closes %d, transfers %d -> %d", b.closes, before, b.transfers)
	}
	if err := c.Close(); err != nil || b.closes != 1 || b.transfers != before+1 {
		t.Fatalf("repeated terminal cleanup: %v", err)
	}
}

func TestLiveRestorationStateRetainsOwner(t *testing.T) {
	c, b := openTerminalBench(t)
	restoreErr := errors.New("live timeout")
	b.wireErr = restoreErr
	if err := c.Close(); !errors.Is(err, restoreErr) || errors.Is(err, armdebug.ErrRestorationAbandoned) || b.closes != 0 {
		t.Fatalf("abandoned live restoration: %v", err)
	}
	b.wireErr = nil
	if err := c.Close(); err != nil || b.closes != 1 {
		t.Fatalf("live retry: %v", err)
	}
	if b.target.OverrunDetectEnabled() {
		t.Fatal("lost ORUNDETECT restoration")
	}
}

func TestTerminalUnownedEntryReturnsNoCleanupOwner(t *testing.T) {
	b := &terminalBench{bench: newBench(), possible: true, pending: true}
	operationErr := errors.New("first entry failed")
	b.beforeTransfer = func(context.Context) { b.possible = false; b.wireErr = operationErr }
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if c != nil || !errors.Is(err, operationErr) || errors.Is(err, armdebug.ErrRestorationAbandoned) || b.closes != 1 || b.transfers != 1 {
		t.Fatalf("manufactured unowned restoration: owner %v, error %v, closes %d, transfers %d", c, err, b.closes, b.transfers)
	}
}
