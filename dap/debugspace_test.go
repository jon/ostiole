package dap_test

import (
	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"testing"
)

func TestDebugSpaceBaseAndReads(t *testing.T) {
	target := newWaitTarget()
	target.Target = dapsim.New(0x4c013477)
	for reg, value := range map[dap.DPRegister]uint32{dap.DPIDR1: 40, dap.BASEPTR0: 0x2001, dap.BASEPTR1: 1} {
		if err := target.SetDPRegister(reg, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := target.SetDebugWord(0x100002ff0, 0xd); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	space := dp.DebugSpace()
	base, present, err := space.ReadDebugBase(t.Context())
	if err != nil || !present || base != 0x100002000 {
		t.Fatalf("base=%#x,%v,%v", base, present, err)
	}
	if got, err := space.ReadScalar(t.Context(), base+0xff0, dap.Size32); err != nil || got != 0xd {
		t.Fatalf("word=%#x,%v", got, err)
	}
	before := len(target.requests)
	for _, size := range []dap.TransferSize{0, dap.Size8, dap.Size16, dap.Size64} {
		if _, err := space.ReadScalar(t.Context(), base, size); err == nil {
			t.Fatal("unsupported size accepted")
		}
	}
	if _, err := space.ReadScalar(t.Context(), 1, dap.Size32); err == nil {
		t.Fatal("unaligned read")
	}
	if _, err := space.ReadScalar(t.Context(), 1<<40, dap.Size32); err == nil {
		t.Fatal("out-of-range read")
	}
	if len(target.requests) != before {
		t.Fatal("invalid read sent traffic")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := space.ReadScalar(t.Context(), base, dap.Size32); err == nil {
		t.Fatal("released space read")
	}
}

func TestDebugSpaceBaseValidation(t *testing.T) {
	for _, tt := range []struct {
		name             string
		low              uint32
		present, invalid bool
	}{
		{"zero", 1, true, false}, {"absent", 0, false, false}, {"reserved", 3, false, true}, {"outside", 0x100001, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			target := newWaitTarget()
			target.Target = dapsim.New(0x4c013477)
			if err := target.SetDPRegister(dap.DPIDR1, 20); err != nil {
				t.Fatal(err)
			}
			if err := target.SetDPRegister(dap.BASEPTR0, tt.low); err != nil {
				t.Fatal(err)
			}
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			address, present, err := dp.DebugSpace().ReadDebugBase(t.Context())
			if address != 0 || present != tt.present || (err != nil) != tt.invalid {
				t.Fatalf("base=%#x,%v,%v", address, present, err)
			}
			if err := dp.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDebugSpaceRejectsUnavailableOwner(t *testing.T) {
	var zero dap.DebugSpace
	if _, _, err := zero.ReadDebugBase(t.Context()); err == nil {
		t.Fatal("zero reader accepted")
	}
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	if _, _, err := dp.DebugSpace().ReadDebugBase(t.Context()); err == nil {
		t.Fatal("ADIv5 space accepted")
	}
	if len(target.requests) != before {
		t.Fatal("ADIv5 request sent traffic")
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
