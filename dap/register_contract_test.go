package dap_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
)

func TestRegisterMEMAPSessionOwnership(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	sel := dap.NewAPSel(1)
	first, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	before := len(model.requests)
	if duplicate, err := dap.OpenMemAP(t.Context(), dp, sel); err == nil || duplicate != nil || len(model.requests) != before {
		t.Fatalf("duplicate=%v,%v traffic=%d/%d", duplicate, err, len(model.requests), before)
	}
	if err := first.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	before = len(model.requests)
	if _, err := dap.OpenMemAP(t.Context(), dp, sel); err == nil || len(model.requests) != before {
		t.Fatal("restoration freed AP reservation")
	}
	if _, err := first.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	requireOldRegisterSessionRejected(t, model, first)
	if value, err := current.ReadWord(t.Context(), 0x100); err != nil || value != 42 {
		t.Fatalf("new owner=%d,%v", value, err)
	}
	if err := current.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterConnectedPreflightSendsNothing(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	before := len(model.requests)
	if _, err := dp.ReadDP(t.Context(), dap.DPRegister(0)); err == nil {
		t.Fatal("zero DP register accepted")
	}
	if _, err := dp.ReadRawAP(t.Context(), dap.APAddress{}); err == nil {
		t.Fatal("zero AP address accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := dp.ReadAPIDR(ctx, dap.NewAPSel(1)); err == nil {
		t.Fatal("canceled access succeeded")
	}
	if err := dp.Release(ctx); err == nil {
		t.Fatal("canceled release succeeded")
	}
	if model.connects != 1 || model.releases != 0 || len(model.requests) != before || !dp.CleanupPending() {
		t.Fatal("invalid/canceled request changed protocol state")
	}
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
		t.Fatalf("usable owner became cleanup-only: %v", err)
	}
}

func TestRegisterErrorsNameLogicalAPAddress(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	model.failFor = apRead(8)
	model.failStatus = dap.RegisterRejected
	model.failCause = context.Canceled
	_, err := dp.ReadRawAP(t.Context(), dap.NewAPSel(1).Address(0xf8))
	if err == nil || !strings.Contains(err.Error(), "AP1 register 0xf8") {
		t.Fatalf("logical register attribution=%v", err)
	}
}

func TestRegisterContradictoryErrorClassesAreProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		status dap.RegisterStatus
		cause  error
	}{
		{dap.RegisterIndeterminate, errors.Join(dap.ErrIndeterminate, dap.ErrNotExecuted)},
		{dap.RegisterUnsent, dap.ErrFault},
		{dap.RegisterRejected, swd.ErrParity},
		{dap.RegisterCompleted, dap.ErrWait},
	} {
		model := newRegisterModel(t)
		dp := dap.NewDebugPort(dap.RegisterDP(model))
		if _, err := dp.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		releaseErrorPort(t, dp)
		model.failFor = apRead(12)
		model.failStatus = tc.status
		model.failCause = tc.cause
		_, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
		if !errors.Is(err, dap.ErrProtocol) || !errors.Is(err, dap.ErrIndeterminate) {
			t.Errorf("status=%d cause=%v outcome=%v", tc.status, tc.cause, err)
		}
	}
}

func TestRegisterWideSelectionAndRelease(t *testing.T) {
	for _, bits := range []uint8{40, 48, 52} {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			target := dapsim.New(0x4c013477)
			if err := target.SetDPRegister(dap.DPIDR1, uint32(bits)); err != nil {
				t.Fatal(err)
			}
			sel, err := dap.APAt(uint64(1)<<(bits-4) | 0x2000)
			if err != nil {
				t.Fatal(err)
			}
			if err := target.AddMEMAP(sel, 0x24770011, map[uint32]uint32{0x100: 42}); err != nil {
				t.Fatal(err)
			}
			model := &registerModel{target: target}
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			mem, err := dap.OpenMemAP(t.Context(), dp, sel)
			if err != nil {
				t.Fatal(err)
			}
			if value, err := mem.ReadWord(t.Context(), 0x100); err != nil || value != 42 {
				t.Fatalf("wide memory=%d,%v", value, err)
			}
			if err := mem.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := dp.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := len(model.requests)
			if err := dp.Release(t.Context()); err != nil || len(model.requests) != before {
				t.Fatalf("idle release=%v", err)
			}
		})
	}
}

func requireOldRegisterSessionRejected(t *testing.T, model *registerModel, first *dap.MemAP) {
	t.Helper()
	before := len(model.requests)
	if _, err := first.ReadWord(t.Context(), 0x100); err == nil {
		t.Fatal("old session read succeeded")
	}
	if err := first.Release(t.Context()); err == nil {
		t.Fatal("old session restoration succeeded")
	}
	if len(model.requests) != before {
		t.Fatal("old session sent traffic")
	}
}
