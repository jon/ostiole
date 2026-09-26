package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
)

func TestMEMAPWriteWord(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, nil)
	mem, err := dap.OpenMemAP(t.Context(), enteredDAPClient(t, target), apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.WriteWord(t.Context(), 0x100, 0x12345678); err != nil {
		t.Fatal(err)
	}
	if got, err := mem.ReadWord(t.Context(), 0x100); err != nil || got != 0x12345678 {
		t.Fatalf("ReadWord = %#x, %v", got, err)
	}
	if err := mem.WriteWord(t.Context(), 0x101, 0); err == nil {
		t.Fatal("unaligned WriteWord succeeded")
	}
}
