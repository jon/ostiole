package dap_test

import (
	"context"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestPortBindingsValidateBeforeTraffic(t *testing.T) {
	_, wire, chain := jtagModelChain(t, 4, 0)
	_, wideWire, wide := jtagModelChain(t, 5, 0)
	for _, binding := range []dap.Port{{}, dap.SWDP(nil), dap.JTAGDP(nil, 0), dap.JTAGDP(chain, -1), dap.JTAGDP(chain, 2), dap.JTAGDP(wide, 0)} {
		dp := dap.NewDebugPort(binding)
		if _, err := dp.Connect(t.Context()); err == nil {
			t.Fatal("invalid binding connected")
		}
		if err := dp.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if wire.calls != 0 || wideWire.calls != 0 {
		t.Fatal("invalid binding sent traffic")
	}
}

func TestJTAGDPConnectionAndBaselineRegisters(t *testing.T) {
	for _, ir := range []int{4, 8} {
		for position := range 2 {
			exerciseJTAGDPRegisters(t, ir, position)
		}
	}
}

func TestJTAGDPRejectsAPAndTransactionsBeforeTraffic(t *testing.T) {
	_, wire, chain := jtagModelChain(t, 4, 0)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	if _, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1)); err == nil {
		t.Fatal("accepted AP access")
	}
	txn := dp.NewTxn()
	result := txn.ReadDP(dap.CTRLSTAT)
	if err := txn.Commit(t.Context()); err == nil {
		t.Fatal("accepted transaction")
	}
	if _, err := result.Value(); err == nil {
		t.Fatal("unexecuted result succeeded")
	}
	if wire.calls != before {
		t.Fatal("unsupported operation sent traffic")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func exerciseJTAGDPRegisters(t *testing.T, ir, position int) {
	t.Helper()
	m, wire, chain := jtagModelChain(t, ir, position)
	m.ctrl = 1 | 1<<28 | 1<<29 | 1<<5
	dp := dap.NewDebugPort(dap.JTAGDP(chain, position))
	if wire.calls != 0 {
		t.Fatal("constructor sent traffic")
	}
	identity, err := dp.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := identity.IDCODE(); !ok || id != 0x5ba00477 {
		t.Fatalf("IDCODE = %#x, %v", id, ok)
	}
	if _, ok := identity.DPIDR(); ok {
		t.Fatal("fabricated DPIDR")
	}
	if m.ctrl != 0xf0000000 {
		t.Fatalf("connected CTRL/STAT = %#x", m.ctrl)
	}
	readJTAGDPRegisters(t, dp)
	before := wire.calls
	rejectJTAGDPRegisters(t, dp, m.ctrl)
	if wire.calls != before {
		t.Fatal("invalid register operation sent traffic")
	}
	if err := dp.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.ctrl != 1|1<<28|1<<29 {
		t.Fatalf("restored CTRL/STAT = %#x", m.ctrl)
	}
	before = wire.calls
	if err := dp.Release(t.Context()); err != nil || wire.calls != before {
		t.Fatal("repeated release sent traffic")
	}
}

func readJTAGDPRegisters(t *testing.T, dp *dap.DebugPort) {
	t.Helper()
	for _, value := range []uint32{0, 0x010000f0} {
		if err := dp.WriteDP(t.Context(), dap.SELECT, value); err != nil {
			t.Fatal(err)
		}
		if got, err := dp.ReadDP(t.Context(), dap.SELECT); err != nil || got != value {
			t.Fatalf("SELECT = %#x, %v", got, err)
		}
	}
	if got, err := dp.ReadDP(t.Context(), dap.IDCODE); err != nil || got != 0x5ba00477 {
		t.Fatalf("IDCODE read = %#x, %v", got, err)
	}
	if got, err := dp.ReadDP(t.Context(), dap.RDBUFF); err != nil || got != 0 {
		t.Fatalf("RDBUFF read = %#x, %v", got, err)
	}
}

func rejectJTAGDPRegisters(t *testing.T, dp *dap.DebugPort, ctrl uint32) {
	t.Helper()
	for _, reg := range []dap.DPRegister{dap.DPIDR, dap.RESEND, dap.DLCR, dap.TARGETID, dap.DLPIDR, dap.EVENTSTAT} {
		if _, err := dp.ReadDP(t.Context(), reg); err == nil {
			t.Fatalf("accepted %v", reg)
		}
	}
	for _, value := range []uint32{0, 2, 0x1f} {
		if err := dp.WriteDP(t.Context(), dap.ABORT, value); err == nil {
			t.Fatalf("accepted ABORT %#x", value)
		}
	}
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, ctrl|1<<2); err == nil {
		t.Fatal("enabled transaction mode")
	}
}
