package dap_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestSWDRejectsTransactionModesBeforeTraffic(t *testing.T) {
	for _, dpidr := range []uint32{0x2ba01477, 0x4c013477} {
		for _, bit := range []uint{2, 3, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23} {
			t.Run(fmt.Sprintf("%x/bit%d", dpidr, bit), func(t *testing.T) {
				testSWDModeWrite(t, dpidr, 1<<bit)
			})
		}
	}
}

func testSWDModeWrite(t *testing.T, dpidr, mode uint32) {
	t.Helper()
	target := newWaitTarget()
	target.Target = sim.New(dpidr)
	if dpidr == 0x4c013477 {
		if err := target.SetDPRegister(dap.DPIDR1, 32); err != nil {
			t.Fatal(err)
		}
	}
	wire := &packedTxnWire{inner: swdsim.New(target), limit: 54}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(t.Context(), dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	before := len(wire.calls)
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, state|mode); err == nil || !strings.Contains(err.Error(), "transaction") {
		t.Errorf("immediate mode write = %v, want transaction-mode rejection", err)
	}
	txn := dp.NewTxn()
	prefix := txn.ReadDP(dap.DPIDR)
	result := txn.WriteDP(dap.CTRLSTAT, state|mode)
	if err := txn.Commit(t.Context()); err == nil || !strings.Contains(err.Error(), "transaction") {
		t.Errorf("queued mode write = %v, want transaction-mode rejection", err)
	}
	if _, err := prefix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
		t.Errorf("invalid queue prefix = %v, want unsent", err)
	}
	if err := result.Err(); err == nil || !strings.Contains(err.Error(), "transaction") {
		t.Errorf("queued write result = %v, want transaction-mode rejection", err)
	}
	if len(wire.calls) != before {
		t.Errorf("invalid mode writes emitted %d wire calls", len(wire.calls)-before)
	}
	if got, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil || got != state {
		t.Errorf("state after rejected writes = %#x, %v; want %#x", got, err, state)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type inheritedModeTarget struct {
	*waitTarget
	mode uint32
}

func (t *inheritedModeTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := t.waitTarget.Read(ctx, req)
	bank := uint32(0)
	if len(t.selectValues) != 0 {
		bank = t.selectValues[len(t.selectValues)-1] & 15
	}
	if req == dpRead(4) && bank == 0 {
		value |= t.mode
	}
	return value, err
}

func TestSWDRejectsInheritedModesBeforeAcquiringPower(t *testing.T) {
	for _, dpidr := range []uint32{0x2ba01477, 0x4c013477} {
		for _, mode := range []uint32{1 << 2, 2 << 2, 3 << 2, 1 << 12, 0xfff << 12} {
			for _, inheritedPower := range []bool{false, true} {
				t.Run(fmt.Sprintf("%x/%x/power%t", dpidr, mode, inheritedPower), func(t *testing.T) {
					testInheritedSWDMode(t, dpidr, mode, inheritedPower, "transaction")
				})
			}
		}
	}
}

func TestDPv3RejectsInheritedERRMODEWithInheritedPower(t *testing.T) {
	testInheritedSWDMode(t, 0x4c013477, 1<<24, true, "ERRMODE")
}

func testInheritedSWDMode(t *testing.T, dpidr, mode uint32, inheritedPower bool, wantError string) {
	t.Helper()
	target := &inheritedModeTarget{waitTarget: newWaitTarget(), mode: mode}
	target.Target = sim.New(dpidr)
	if dpidr == 0x4c013477 {
		if err := target.SetDPRegister(dap.DPIDR1, 32); err != nil {
			t.Fatal(err)
		}
	}
	power := uint32(0)
	if inheritedPower {
		power = debugRequest | systemRequest
	}
	if err := target.Target.Write(t.Context(), dpWrite(4), power|1); err != nil {
		t.Fatal(err)
	}
	wire := &packedTxnWire{inner: swdsim.New(target), limit: 54}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err == nil || !strings.Contains(err.Error(), wantError) {
		t.Errorf("inherited mode Connect = %v, want %s rejection", err, wantError)
	}
	if dp.CleanupPending() {
		t.Error("failed mode entry retained cleanup after successful restoration")
	}
	for _, req := range target.requests {
		if req.AP || req == dpWrite(4) {
			t.Errorf("rejected inherited mode sent AP or CTRL/STAT write: %+v", req)
		}
	}
	state, err := target.Target.Read(t.Context(), dpRead(4))
	if err != nil || state&(debugRequest|systemRequest) != power {
		t.Errorf("inherited power changed: %#x, %v; want requests %#x", state, err, power)
	}
	before := len(wire.calls)
	if err := dp.Release(t.Context()); err != nil || len(wire.calls) != before {
		t.Errorf("completed entry cleanup Release = %v, traffic delta %d", err, len(wire.calls)-before)
	}
}

func TestLogicalDPTxnRejectsWrongDirectionBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	wire := &packedTxnWire{inner: swdsim.New(target), limit: 54}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	for _, reg := range []dap.DPRegister{dap.ABORT, dap.SELECT, dap.DPIDR, dap.TARGETID, dap.DLPIDR, dap.EVENTSTAT, dap.RESEND, dap.RDBUFF} {
		before := len(wire.calls)
		txn := dp.NewTxn()
		prefix := txn.ReadDP(dap.DPIDR)
		var resultErr func() error
		if reg == dap.ABORT || reg == dap.SELECT {
			result := txn.ReadDP(reg)
			resultErr = func() error { _, err := result.Value(); return err }
		} else {
			resultErr = txn.WriteDP(reg, 0).Err
		}
		if err := txn.Commit(t.Context()); err == nil {
			t.Errorf("wrong-direction %s transaction succeeded", reg)
		}
		if err := resultErr(); err == nil {
			t.Errorf("wrong-direction %s result succeeded", reg)
		}
		if _, err := prefix.Value(); !errors.Is(err, dap.ErrNotExecuted) {
			t.Errorf("wrong-direction %s queue sent prefix: %v", reg, err)
		}
		if len(wire.calls) != before {
			t.Errorf("wrong-direction %s queue emitted %d wire calls", reg, len(wire.calls)-before)
		}
	}
}
