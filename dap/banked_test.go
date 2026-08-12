package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
)

func TestBankedDPAccessPreservesSELECTFields(t *testing.T) {
	target := newWaitTarget()
	target.AddAP(0x12, 0x24770011)
	const dlcr = uint32(0xa5a50000)
	if err := target.SetBankedDPRegister(1, dlcr); err != nil {
		t.Fatal(err)
	}
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 0x120000f0); err != nil {
		t.Fatal(err)
	}
	beforeBankedRead := len(target.requests)
	value, err := dp.ReadDPAt(t.Context(), dap.DLCRAddr)
	if err != nil {
		t.Fatal(err)
	}
	if value != dlcr {
		t.Fatalf("DLCR = %#08x, want %#08x", value, dlcr)
	}
	wantRequests := []swd.Request{{Addr: uint8(dap.SELECT)}, {Read: true, Addr: uint8(dap.RDBUFF)}, {Read: true, Addr: uint8(dap.CTRLSTAT)}}
	if got := target.requests[beforeBankedRead : beforeBankedRead+len(wantRequests)]; !equalRequests(got, wantRequests) {
		t.Fatalf("banked DP requests = %#v, want %#v", got, wantRequests)
	}
	if _, err := dp.ReadAP(t.Context(), 0x12, dap.APIDR); err != nil {
		t.Fatal(err)
	}
	if len(target.selectValues) < 3 {
		t.Fatalf("SELECT writes = %#v", target.selectValues)
	}
	got := target.selectValues[len(target.selectValues)-2:]
	want := []uint32{0x120000f1, 0x120000f0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SELECT writes = %#v, want suffix %#v", target.selectValues, want)
		}
	}
}

func equalRequests(got, want []swd.Request) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestBankedDPAccessRejectsUnsupportedVersionWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	if _, err := dp.ReadDPAt(t.Context(), dap.TARGETIDAddr); err == nil {
		t.Fatal("DPv1 TARGETID read succeeded")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after rejected banked read = %d, want %d", got, before)
	}
}

func TestBankedDPAccessRejectsDPv3WithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	target.dpidrOverride = 0x2ba03477
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	if _, err := dp.ReadDPAt(t.Context(), dap.DLCRAddr); err == nil {
		t.Fatal("DPv3 DLCR read succeeded through the ADIv5 address map")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after rejected DPv3 read = %d, want %d", got, before)
	}

}

func TestBankedDPAccessAllowsMinimalDebugPort(t *testing.T) {
	target := newWaitTarget()
	target.dpidrOverride = 0x2ba11477
	const dlcr = uint32(0xa5a50000)
	if err := target.SetBankedDPRegister(1, dlcr); err != nil {
		t.Fatal(err)
	}
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	value, err := dp.ReadDPAt(t.Context(), dap.DLCRAddr)
	if err != nil {
		t.Fatal(err)
	}
	if value != dlcr {
		t.Fatalf("DLCR = %#08x, want %#08x", value, dlcr)
	}
}

func TestExplicitDPAccessRejectsReadOnlyWriteWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	target.dpidrOverride = 0x2ba02477
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, addr := range []dap.DPAddress{dap.RDBUFFAddr, dap.TARGETIDAddr, dap.DLPIDRAddr, dap.EVENTSTATAddr} {
		before := len(target.requests)
		if err := dp.WriteDPAt(t.Context(), addr, 0); err == nil {
			t.Fatalf("WriteDPAt(%+v) succeeded", addr)
		}
		if got := len(target.requests); got != before {
			t.Fatalf("requests after rejected explicit write = %d, want %d", got, before)
		}
	}
}

func TestExplicitDPAccessRejectsUnsupportedFramingBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDPAt(t.Context(), dap.DLCRAddr); err != nil {
		t.Fatal(err)
	}

	before := len(target.requests)
	if err := dp.WriteDPAt(t.Context(), dap.CTRLSTATAddr, overrunDetect); err == nil {
		t.Fatal("WriteDPAt(CTRLSTAT) accepted unsupported ORUNDETECT")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after rejected CTRL/STAT write = %d, want %d", got, before)
	}

	if err := dp.WriteDPAt(t.Context(), dap.DLCRAddr, 1<<8); err == nil {
		t.Fatal("WriteDPAt(DLCR) accepted unsupported turnaround")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after rejected DLCR write = %d, want %d", got, before)
	}
	if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, 1<<8); err == nil {
		t.Fatal("WriteDP(CTRLSTAT) accepted unsupported turnaround in DP bank 1")
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after rejected raw DLCR write = %d, want %d", got, before)
	}
}
