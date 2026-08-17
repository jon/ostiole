package sim_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
)

func apSel(value uint8) dap.APSel {
	return dap.NewAPSel(value)
}

func addAPFixture(t testing.TB, target *sim.Target, sel uint8, idr uint32) {
	t.Helper()
	if err := target.AddAP(apSel(sel), idr); err != nil {
		t.Fatal(err)
	}
}

func addMEMAPFixture(t testing.TB, target *sim.Target, sel uint8, idr uint32, words map[uint32]uint32) {
	t.Helper()
	if err := target.AddMEMAP(apSel(sel), idr, words); err != nil {
		t.Fatal(err)
	}
}
