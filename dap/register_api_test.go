package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestLogicalDPRegistersDistinguishSharedWireOffsets(t *testing.T) {
	for _, pair := range [][2]dap.DPRegister{
		{dap.DPIDR, dap.ABORT},
		{dap.CTRLSTAT, dap.DLCR},
		{dap.SELECT, dap.RESEND},
	} {
		if pair[0] == pair[1] {
			t.Fatalf("logical DP registers compare equal: %v and %v", pair[0], pair[1])
		}
	}
}

func TestLogicalDPAccessRejectsWrongDirectionBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)

	for _, reg := range []dap.DPRegister{dap.DPIDR, dap.TARGETID, dap.DLPIDR, dap.EVENTSTAT, dap.RESEND, dap.RDBUFF} {
		before := len(target.requests)
		if err := dp.WriteDP(t.Context(), reg, 0); err == nil {
			t.Fatalf("WriteDP(%v) succeeded", reg)
		}
		if got := len(target.requests); got != before {
			t.Fatalf("requests after WriteDP(%v) = %d, want %d", reg, got, before)
		}
	}
	for _, reg := range []dap.DPRegister{dap.ABORT, dap.SELECT} {
		before := len(target.requests)
		if _, err := dp.ReadDP(t.Context(), reg); err == nil {
			t.Fatalf("ReadDP(%v) succeeded", reg)
		}
		if got := len(target.requests); got != before {
			t.Fatalf("requests after ReadDP(%v) = %d, want %d", reg, got, before)
		}
	}

	for _, reg := range []dap.DPRegister{0, 0xffff} {
		before := len(target.requests)
		if _, err := dp.ReadDP(t.Context(), reg); err == nil {
			t.Fatalf("ReadDP(%v) succeeded", reg)
		}
		if err := dp.WriteDP(t.Context(), reg, 0); err == nil {
			t.Fatalf("WriteDP(%v) succeeded", reg)
		}
		if got := len(target.requests); got != before {
			t.Fatalf("requests after invalid register %v = %d, want %d", reg, got, before)
		}
	}
}

func TestLogicalDPAccessDistinguishesBankIndependentAndBankZero(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0x120000f1); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.RDBUFF); err != nil {
		t.Fatal(err)
	}

	selects := len(target.selectValues)
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err != nil {
		t.Fatal(err)
	}
	if got := len(target.selectValues); got != selects {
		t.Fatalf("SELECT writes after bank-independent DPIDR = %d, want %d", got, selects)
	}

	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err != nil {
		t.Fatal(err)
	}
	if got := target.selectValues[len(target.selectValues)-1]; got != 0x120000f0 {
		t.Fatalf("SELECT for bank-zero CTRL/STAT = %#08x, want 0x120000f0", got)
	}
}
