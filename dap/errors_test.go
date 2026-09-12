package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type portErrorTarget struct {
	*sim.Target
	cause error
}

func (target *portErrorTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	if req.AP {
		return target.cause
	}
	return target.Target.Acknowledge(ctx, req)
}

func TestPortErrorClassificationPreservesSWDCauses(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		want  error
	}{
		{"WAIT", swd.ErrWait, dap.ErrWait},
		{"FAULT", swd.ErrFault, dap.ErrFault},
		{"protocol", swd.ErrProtocol, dap.ErrProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := &portErrorTarget{Target: sim.New(0x2ba01477), cause: test.cause}
			addAP(t, target, 0, 0x24770011)
			dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			_, err := dp.ReadAPIDR(t.Context(), apSel(0))
			if !errors.Is(err, test.want) || !errors.Is(err, test.cause) {
				t.Fatalf("read error = %v; want %v and %v", err, test.want, test.cause)
			}
			target.cause = nil
			if err := dp.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueuedErrorsRetainProtocolIndependentClassification(t *testing.T) {
	target := &portErrorTarget{Target: sim.New(0x2ba01477), cause: swd.ErrWait}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))), dap.WithMaxWaits(1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	read := txn.ReadAPIDR(apSel(0))
	unsent := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); !errors.Is(err, dap.ErrWait) || !errors.Is(err, swd.ErrWait) {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := read.Value(); !errors.Is(err, dap.ErrWait) || !errors.Is(err, swd.ErrWait) {
		t.Fatalf("queued read: %v", err)
	}
	if _, err := unsent.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Fatalf("unsent read: %v", err)
	}
	target.cause = nil
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSuccessfulPortReadHasNoError(t *testing.T) {
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(sim.New(0x2ba01477)))))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
