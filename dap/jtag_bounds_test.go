package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/jtag"
)

func TestJTAGDPRejectsBypassBindingBeforeTraffic(t *testing.T) {
	_, wire, _ := jtagModelChain(t, 4, 0)
	first, err := jtag.Bypass(4)
	if err != nil {
		t.Fatal(err)
	}
	second, err := jtag.IDCODE(12, 0x14730093)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := jtag.NewChain(jtag.New(wire), jtag.Layout{first, second})
	if err != nil {
		t.Fatal(err)
	}
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err == nil || wire.calls != 0 {
		t.Fatalf("BYPASS binding: error=%v calls=%d", err, wire.calls)
	}
	if err := dp.Release(t.Context()); err != nil || wire.calls != 0 {
		t.Fatalf("BYPASS cleanup: error=%v calls=%d", err, wire.calls)
	}
}

func TestJTAGSELECTReservedFieldsRejectBeforeTraffic(t *testing.T) {
	_, wire, chain := jtagModelChain(t, 8, 1)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 1))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := wire.calls
	for _, value := range []uint32{1, 8, 15, 1 << 8, 1 << 23, 0x00ffff00, 0x010001f0} {
		if err := dp.WriteDP(t.Context(), dap.SELECT, value); err == nil {
			t.Fatalf("direct SELECT%#x accepted", value)
		}
		txn := dp.NewTxn()
		txn.WriteDP(dap.SELECT, value)
		if err := txn.Commit(t.Context()); err == nil {
			t.Fatalf("queued SELECT%#x accepted", value)
		}
		if wire.calls != before {
			t.Fatal("invalid SELECT reached wire")
		}
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGModelRequiresCompleteEightBitInstruction(t *testing.T) {
	for _, row := range []struct {
		instruction uint64
		bits        int
	}{
		{0xf8, 35}, {0xfa, 35}, {0xfb, 35}, {0xfe, 32},
		{0x08, 1}, {0x0a, 1}, {0x0b, 1}, {0x0e, 1},
		{0x7a, 1}, {0xff, 1},
	} {
		model := &jtagDPModel{}
		wire := &jtagDPWire{state: jtag.CaptureDR, taps: []dpModelTAP{{ir: 8, id: 0x5ba00477, instruction: row.instruction, dp: model}}}
		wire.capture()
		if len(wire.shift) != row.bits {
			t.Errorf("instruction%#x captured%d bits want%d", row.instruction, len(wire.shift), row.bits)
		}
	}
}
