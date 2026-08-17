package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestAccessSelectedAPRegisters(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddAP(0, 0x04770031)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	idr, err := dp.ReadAP(t.Context(), 0, dap.APIDR)
	if err != nil {
		t.Fatal(err)
	}
	if idr != 0x04770031 {
		t.Fatalf("AP0 IDR = %#08x, want 0x04770031", idr)
	}
	absent, err := dp.ReadAP(t.Context(), 1, dap.APIDR)
	if err != nil {
		t.Fatal(err)
	}
	if absent != 0 {
		t.Fatalf("absent AP1 IDR = %#08x, want 0", absent)
	}

	if err := dp.WriteAP(t.Context(), 0, 0, 0x12345678); err != nil {
		t.Fatal(err)
	}
	value, err := dp.ReadAP(t.Context(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x12345678 {
		t.Fatalf("AP0 register 0 = %#08x, want 0x12345678", value)
	}
}

func TestAPAccessRequiresConnectedDebugPort(t *testing.T) {
	dp := enteredDP(t, sim.New(0x2ba01477))
	if _, err := dp.ReadAP(t.Context(), 0, dap.APIDR); err == nil {
		t.Fatal("ReadAP() succeeded before Connect()")
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteAP(t.Context(), 0, 0, 1); err == nil {
		t.Fatal("WriteAP() succeeded after Release()")
	}
}

func TestAPWriteWaitsForRDBUFF(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	target.AddAP(0, 0x04770031)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})
	target.failBarrier = true
	if err := dp.WriteAP(t.Context(), 0, 0, 1); !errors.Is(err, errBarrier) {
		t.Fatalf("WriteAP() error = %v, want %v", err, errBarrier)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil {
		t.Fatal("ReadDP() succeeded after the AP completion transfer failed")
	}
}

var errBarrier = errors.New("posted write failed")

type barrierTarget struct {
	*sim.Target
	failBarrier bool
	postedWrite bool
}

func (t *barrierTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	if !req.AP && req.Addr == 0x0c && t.postedWrite && t.failBarrier {
		t.postedWrite = false
		return 0, errBarrier
	}
	return t.Target.Read(ctx, req)
}

func (t *barrierTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req.AP {
		t.postedWrite = true
	}
	return t.Target.Write(ctx, req, value)
}

var _ swdsim.Target = (*barrierTarget)(nil)
