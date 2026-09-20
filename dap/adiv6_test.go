package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestDPv3BankedIdentity(t *testing.T) {
	target := dapsim.New(0x4c013477)
	for reg, value := range map[dap.DPRegister]uint32{dap.DPIDR1: 32, dap.BASEPTR0: 0x1001, dap.BASEPTR1: 0, dap.DLCR: 0} {
		if err := target.SetDPRegister(reg, value); err != nil {
			t.Fatal(err)
		}
	}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	for _, reg := range []dap.DPRegister{dap.DPIDR1, dap.BASEPTR0, dap.BASEPTR1, dap.DPIDR} {
		got, err := dp.ReadDP(t.Context(), reg)
		want := map[dap.DPRegister]uint32{dap.DPIDR1: 32, dap.BASEPTR0: 0x1001, dap.BASEPTR1: 0, dap.DPIDR: 0x4c013477}[reg]
		if err != nil || got != want {
			t.Fatalf("%s = %#x, %v; want %#x", reg, got, err, want)
		}
	}
	txn := dp.NewTxn()
	base := txn.ReadDP(dap.BASEPTR0)
	id := txn.ReadDP(dap.DPIDR)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := base.Value(); err != nil || got != 0x1001 {
		t.Fatalf("base = %#x, %v", got, err)
	}
	if got, err := id.Value(); err != nil || got != 0x4c013477 {
		t.Fatalf("identity = %#x, %v", got, err)
	}
}

func TestADIv6RegistersRejectADIv5(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	for _, reg := range []dap.DPRegister{dap.DPIDR1, dap.BASEPTR0, dap.BASEPTR1, dap.SELECT1} {
		if _, err := dp.ReadDP(t.Context(), reg); err == nil {
			t.Fatalf("read %s accepted", reg)
		}
		if err := dp.WriteDP(t.Context(), reg, 0); err == nil {
			t.Fatalf("write %s accepted", reg)
		}
	}
	if len(target.requests) != before {
		t.Fatal("invalid registers sent traffic")
	}
}
