package sim

import (
	"testing"

	"github.com/jon/ostiole/dap"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestTargetZeroValueAcceptsAPFixtures(t *testing.T) {
	t.Run("AP", func(t *testing.T) {
		var target Target
		if err := target.AddAP(dap.NewAPSel(0), 0x04770031); err != nil {
			t.Fatal(err)
		}
		if err := target.AddAP(dap.NewAPSel(0), 0x04770031); err == nil {
			t.Fatal("duplicate AP accepted")
		}
	})
	t.Run("MEMAP", func(t *testing.T) {
		var target Target
		if err := target.AddMEMAP(dap.NewAPSel(0), 0x24770011, map[uint32]uint32{0x100: 42}); err != nil {
			t.Fatal(err)
		}
		if got, err := target.MEMAPBytes(dap.NewAPSel(0), 0x100, 4); err != nil || got[0] != 42 {
			t.Fatalf("fixture = %v, %v", got, err)
		}
	})
}

func TestTargetRESENDTracksDeliveredPostedValue(t *testing.T) {
	target := postedFixture(t)
	checkTargetRead(t, target, readAPRequest(0), 0)
	checkTargetRead(t, target, readAPRequest(4), 0x11223344)
	checkTargetRead(t, target, readDPRequest(8), 0x11223344)
	checkTargetRead(t, target, readDPRequest(4), 0)
	checkTargetRead(t, target, readDPRequest(8), 0x11223344)
	checkTargetRead(t, target, readDPRequest(12), 0x55667788)
	checkTargetRead(t, target, readDPRequest(8), 0x55667788)
	checkTargetRead(t, target, readDPRequest(12), 0)
	checkTargetRead(t, target, readDPRequest(8), 0)
}

func TestTargetWritesDiscardPostedResult(t *testing.T) {
	for _, test := range []struct {
		name      string
		req       swdsim.Request
		value     uint32
		preserved bool
	}{
		{"AP", swdsim.Request{AP: true, Addr: 4}, 0xaabbccdd, false},
		{"SELECT", writeDPRequest(8), 0, false},
		{"CTRLSTAT", writeDPRequest(4), 0, false},
		{"sticky clear", writeDPRequest(0), 0x1e, false},
		{"DAPABORT", writeDPRequest(0), 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := postedFixture(t)
			checkTargetRead(t, target, readAPRequest(0), 0)
			if err := target.Write(t.Context(), test.req, test.value); err != nil {
				t.Fatal(err)
			}
			want := uint32(0)
			if test.preserved {
				want = 0x11223344
			}
			checkTargetRead(t, target, readDPRequest(12), want)
		})
	}
}

func postedFixture(t *testing.T) *Target {
	t.Helper()
	target := New(0x2ba01477)
	if err := target.AddAP(dap.NewAPSel(0), 0x04770031); err != nil {
		t.Fatal(err)
	}
	for addr, value := range map[uint8]uint32{0: 0x11223344, 4: 0x55667788} {
		if err := target.Write(t.Context(), swdsim.Request{AP: true, Addr: addr}, value); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func checkTargetRead(t *testing.T, target *Target, req swdsim.Request, want uint32) {
	t.Helper()
	got, err := target.Read(t.Context(), req)
	if err != nil || got != want {
		t.Fatalf("read %+v = %#x, %v; want %#x", req, got, err, want)
	}
}

func TestNilTargetRejectsAPFixtures(t *testing.T) {
	var target *Target
	if err := target.AddAP(dap.NewAPSel(0), 0x04770031); err == nil {
		t.Fatal("nil target accepted AP")
	}
	if err := target.AddMEMAP(dap.NewAPSel(0), 0x24770011, nil); err == nil {
		t.Fatal("nil target accepted MEM-AP")
	}
}
