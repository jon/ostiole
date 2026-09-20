package dap_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestAPv2Addressing(t *testing.T) {
	target := dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, 40); err != nil {
		t.Fatal(err)
	}
	low, err := dap.APAt(0x2000)
	if err != nil {
		t.Fatal(err)
	}
	high, err := dap.APAt(0x100002000)
	if err != nil {
		t.Fatal(err)
	}
	for sel, id := range map[dap.APSel]uint32{low: 0x34770008, high: 0x24770011} {
		if err := target.AddAP(sel, id); err != nil {
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
	for _, sel := range []dap.APSel{low, high, low} {
		want := uint32(0x34770008)
		if sel == high {
			want = 0x24770011
		}
		got, err := dp.ReadAPIDR(t.Context(), sel)
		if err != nil || got.Raw != want {
			t.Fatalf("AP IDR = %#x, %v; want %#x", got.Raw, err, want)
		}

	}
}

func TestAPv2RejectsInvalidAddressesBeforeTraffic(t *testing.T) {
	target := newWaitTarget()
	target.Target = dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, 20); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	base, _ := dap.APAt(0x2000)
	outside, _ := dap.APAt(1 << 20)
	for _, addr := range []dap.APAddress{base.Address(0x1000), base.Address(3), outside.Address(0), dap.NewAPSel(0).Address(0xfc)} {
		if _, err := dp.ReadRawAP(t.Context(), addr); err == nil {
			t.Fatalf("accepted %+v", addr)
		}
	}
	if err := dp.WriteRawAP(t.Context(), base.Address(0xdfc), 1); err == nil {
		t.Fatal("wrote APIDR")
	}
	if len(target.requests) != before {
		t.Fatal("rejected access sent traffic")
	}
	if _, err := dap.APAt(1); err == nil {
		t.Fatal("unaligned AP accepted")
	}
	zero, err := dap.APAt(0)
	if err != nil || zero == (dap.APSel{}) || zero == dap.NewAPSel(0) {
		t.Fatal("valid AP zero aliases another selector")
	}
	if _, err := zero.Value(); err == nil {
		t.Fatal("APv2 exposed APSEL")
	}
	if _, err := dap.NewAPSel(0).BaseAddress(); err == nil {
		t.Fatal("APv1 exposed a base")
	}
}

func TestAPv2ReleaseClearsUpperSelection(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", fail), func(t *testing.T) { checkAPv2ReleaseClearsUpperSelection(t, fail) })
	}
}

func checkAPv2ReleaseClearsUpperSelection(t *testing.T, fail bool) {
	t.Helper()
	target := newWaitTarget()
	target.Target = dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, 40); err != nil {
		t.Fatal(err)
	}
	low, _ := dap.APAt(0)
	high, _ := dap.APAt(1 << 32)
	for sel, id := range map[dap.APSel]uint32{low: 0x34770008, high: 0x24770011} {
		if err := target.AddAP(sel, id); err != nil {
			t.Fatal(err)
		}
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadAPIDR(t.Context(), high); err != nil {
		t.Fatal(err)
	}
	if fail {
		target.writeErrFor = dpWrite(4)
		target.writeErr = errors.New("lost SELECT1 response")
		if err := dp.Release(t.Context()); err == nil {
			t.Fatal("release succeeded despite a transfer failure")
		}
		before := len(target.requests)
		assertRepairBlocksTraffic(t, dp, target, before)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Inspect the low AP after release without replacing SELECT1.
	if err := target.Write(t.Context(), dpWrite(8), 0xdf0); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Read(t.Context(), apRead(12)); err != nil {
		t.Fatal(err)
	}
	got, err := target.Read(t.Context(), dpRead(12))
	if err != nil || got != 0x34770008 {
		t.Fatalf("released selection reads %#x, %v; want the low AP", got, err)
	}
}
