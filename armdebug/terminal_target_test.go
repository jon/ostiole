package armdebug_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/target/cortexm"
	coresim "github.com/jon/ostiole/target/cortexm/sim"
)

func haltedTerminalBench(t *testing.T) (*armdebug.Conn, *cortexm.Target, *coresim.Core, *terminalBench) {
	t.Helper()
	b := &terminalBench{bench: newBench(), possible: true, pending: true}
	sel := dap.NewAPSel(0)
	if err := b.target.AddMEMAP(sel, 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	core, err := coresim.New(coresim.Config{Profile: coresim.M0})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []uint64{0xe000ed00, 0xe000ed30, 0xe000edf0} {
		if err := b.target.MapMEMAPDevice(sel, address, 4, core); err != nil {
			t.Fatal(err)
		}
	}
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := c.OpenMemAP(t.Context(), sel)
	if err != nil {
		t.Fatal(err)
	}
	target, err := cortexm.Acquire(t.Context(), memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c, target, core, b
}

func TestTerminalHostCleanupDoesNotResumeBorrowingTarget(t *testing.T) {
	c, target, core, b := haltedTerminalBench(t)
	snapshot := core.Snapshot()
	if snapshot.DHCSR&(1<<17) == 0 {
		t.Fatal("fixture did not halt")
	}
	operationErr := errors.New("probe disappeared during target release")
	hostErr := errors.New("final host error")
	b.possible = false
	b.wireErr = operationErr
	b.closeErr = hostErr
	targetErr := target.Release(t.Context())
	if !errors.Is(targetErr, operationErr) {
		t.Fatalf("lost target release error: %v", targetErr)
	}
	before := b.transfers
	err := errors.Join(targetErr, c.Close())
	if !errors.Is(err, operationErr) || !errors.Is(err, hostErr) || !errors.Is(err, armdebug.ErrRestorationAbandoned) {
		t.Fatalf("lost cleanup errors: %v", err)
	}
	if b.closes != 1 || b.transfers != before || core.Snapshot() != snapshot {
		t.Fatal("host cleanup changed halted target or sent traffic")
	}
	if err := c.Close(); err != nil || b.closes != 1 || b.transfers != before {
		t.Fatalf("repeat host cleanup: %v", err)
	}
}
