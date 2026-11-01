//go:build integration

package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type scratchCleanupWatchdog struct {
	context.Context
	calls int
}

type scratchCleanupLoop struct{}

func (c *scratchCleanupWatchdog) Err() error {
	c.calls++
	if c.calls > 2048 {
		panic(scratchCleanupLoop{})
	}
	return c.Context.Err()
}

type scratchReconnectWire struct {
	swd.Wire
	cancel  context.CancelFunc
	err     error
	onEntry func()
}

func (w *scratchReconnectWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if bits == 136 && w.cancel != nil {
		w.cancel()
		w.cancel = nil
		return nil, errors.Join(w.err, swd.ErrNotExecuted)
	}
	input, err := w.Wire.SWDIO(ctx, direction, output, bits)
	if err == nil && bits == 136 && w.onEntry != nil {
		w.onEntry()
		w.onEntry = nil
	}
	return input, err
}

func TestScratchCleanupStopsWhenReconnectContextExpires(t *testing.T) {
	for _, entry := range []bool{true, false} {
		t.Run(map[bool]string{true: "Connect", false: "ReadAPIDR"}[entry], func(t *testing.T) {
			testScratchReconnectCancellation(t, entry)
		})
	}
}

func testScratchReconnectCancellation(t *testing.T, entry bool) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			if _, ok := recovered.(scratchCleanupLoop); !ok {
				panic(recovered)
			}
			t.Error("scratch cleanup kept retrying after reconnect context cancellation")
		}
	}()
	target := &cancelAPTarget{waitTarget: newWaitTarget(), req: apRead(12)}
	addMEMAP(t, target, 0, 0x00010001, nil)
	wire := &scratchReconnectWire{Wire: swdsim.New(target), err: errors.New("reconnect entry was not sent")}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Error(err)
		}
	})
	target.armFault(apRead(4))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if entry {
		wire.cancel = cancel
	} else {
		wire.onEntry = func() { target.cancel = cancel }
	}
	watchdog := &scratchCleanupWatchdog{Context: ctx}
	var mem *dap.MemAP
	err := restoreHardwareScratch(watchdog, dp, &mem, 0x100, []byte{1}, 0x2ba01477, 0x00010001)
	if !errors.Is(err, context.Canceled) || entry && !errors.Is(err, wire.err) || !errors.Is(err, dap.ErrFault) {
		t.Fatalf("scratch cleanup error = %v, want cancellation and both failed attempts", err)
	}
	if mem != nil {
		t.Fatal("scratch cleanup manufactured a MEM-AP owner")
	}
}
