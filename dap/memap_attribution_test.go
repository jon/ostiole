package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
)

func TestOpenMEMAPAttributesIndexAndBaseAddressErrors(t *testing.T) {
	base, err := dap.APAt(0x100002000)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		dpid uint32
		sel  dap.APSel
		idr  uint32
		want string
	}{
		{"ADIv5 absent", 0x2ba01477, dap.NewAPSel(7), 0, "dap: AP7 is absent"},
		{"ADIv5 non-memory", 0x2ba01477, dap.NewAPSel(7), 1, "dap: AP7 is not a MEM-AP"},
		{"ADIv6 absent", 0x4c013477, base, 0, "dap: AP@0x100002000 is absent"},
		{"ADIv6 non-memory", 0x4c013477, base, 1, "dap: AP@0x100002000 is not a MEM-AP"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := sim.New(test.dpid)
			if test.dpid == 0x4c013477 {
				if err := target.SetDPRegister(dap.DPIDR1, 40); err != nil {
					t.Fatal(err)
				}
			}
			if test.idr != 0 {
				if err := target.AddAP(test.sel, test.idr); err != nil {
					t.Fatal(err)
				}
			}
			memory, err := dap.OpenMemAP(t.Context(), enteredDAPClient(t, target), test.sel)
			if memory != nil || err == nil || err.Error() != test.want {
				t.Fatalf("OpenMemAP()=%v,%v want nil,%q", memory, err, test.want)
			}
		})
	}
}
