package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
)

type accessPortFixture interface {
	AddAP(dap.APSel, uint32) error
}

type memoryAccessPortFixture interface {
	AddMEMAP(dap.APSel, uint32, map[uint32]uint32) error
}

func apSel(value uint8) dap.APSel {
	return dap.NewAPSel(value)
}

func addAP(t testing.TB, target accessPortFixture, sel uint8, idr uint32) {
	t.Helper()
	if err := target.AddAP(apSel(sel), idr); err != nil {
		t.Fatal(err)
	}
}

func addMEMAP(t testing.TB, target memoryAccessPortFixture, sel uint8, idr uint32, words map[uint32]uint32) {
	t.Helper()
	if err := target.AddMEMAP(apSel(sel), idr, words); err != nil {
		t.Fatal(err)
	}
}
