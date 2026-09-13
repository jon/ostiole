package sim

import (
	"github.com/jon/ostiole/dap"
	"testing"
)

func TestMEMAPDebugBaseRegisters(t *testing.T) {
	target := New(0x2ba01477)
	sel := dap.NewAPSel(0)
	if err := target.AddMEMAP(sel, 0x04770021, nil); err != nil {
		t.Fatal(err)
	}
	ap := target.aps[sel]
	if got, err := ap.readRegister(0xf8); err != nil || got != 2 {
		t.Fatalf("default BASE=%#x, %v", got, err)
	}
	if err := target.SetMEMAPDebugBase(sel, 0xe00ff003, 0x12345678); err != nil {
		t.Fatal(err)
	}
	if got, err := ap.readRegister(0xf0); err != nil || got != 0 {
		t.Fatalf("32-bit upper BASE=%#x, %v", got, err)
	}
	if err := target.SetMEMAPCFG(sel, 2); err != nil {
		t.Fatal(err)
	}
	for reg, want := range map[uint8]uint32{0xf8: 0xe00ff003, 0xf0: 0x12345678} {
		if err := ap.writeRegister(reg, 0); err != nil {
			t.Fatal(err)
		}
		if got, err := ap.readRegister(reg); err != nil || got != want {
			t.Fatalf("register %#x=%#x, %v; want %#x", reg, got, err, want)
		}
	}
}

func TestSetMEMAPDebugBaseRequiresMEMAP(t *testing.T) {
	target := New(0x2ba01477)
	sel := dap.NewAPSel(0)
	if err := (*Target)(nil).SetMEMAPDebugBase(sel, 3, 0); err == nil {
		t.Fatal("nil target accepted")
	}
	if err := target.SetMEMAPDebugBase(dap.APSel{}, 3, 0); err == nil {
		t.Fatal("zero selector accepted")
	}
	if err := target.SetMEMAPDebugBase(sel, 3, 0); err == nil {
		t.Fatal("absent AP accepted")
	}
	if err := target.AddAP(sel, 0x02880000); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPDebugBase(sel, 3, 0); err == nil {
		t.Fatal("non-memory AP accepted")
	}
}
