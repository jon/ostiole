package dap_test

import (
	"context"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
)

func TestReadAlignedMEMAPWord(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{
		0xe000ed00: 0x410cc200,
	})
	dp := enteredDAPClient(t, target)
	if err := dp.WriteAP(t.Context(), 0, dap.APReg(0), 0xa5000051); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.NewMemAP(t.Context(), dp, 0)
	if err != nil {
		t.Fatal(err)
	}
	value, err := mem.ReadWord(t.Context(), 0xe000ed00)
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x410cc200 {
		t.Fatalf("ReadWord() = %#08x, want 0x410cc200", value)
	}
	csw, err := dp.ReadAP(t.Context(), 0, dap.APReg(0))
	if err != nil {
		t.Fatal(err)
	}
	if csw != 0xa5000042 {
		t.Fatalf("CSW = %#08x, want preserved value 0xa5000042", csw)
	}
}

func TestMEMAPReleaseRestoresRegisterState(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{
		0xe000ed00: 0x410cc200,
	})
	dp := enteredDAPClient(t, target)
	const (
		originalCSW = uint32(0xa5000051)
		originalTAR = uint32(0x20000000)
	)
	if err := dp.WriteAP(t.Context(), 0, dap.APCSW, originalCSW); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteAP(t.Context(), 0, dap.APTAR, originalTAR); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.NewMemAP(t.Context(), dp, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatal(err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAPRegister(t, dp, dap.APCSW, originalCSW)
	assertAPRegister(t, dp, dap.APTAR, originalTAR)
}

func TestMEMAPReleaseCanRetry(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.NewMemAP(t.Context(), dp, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := mem.Release(canceled); err == nil {
		t.Fatal("Release() succeeded with a canceled context")
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("retry Release(): %v", err)
	}
}

func TestMEMAPRejectsUnalignedWord(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddMEMAP(0, 0x00010001, nil)
	mem, err := dap.NewMemAP(t.Context(), enteredDAPClient(t, target), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 3); err == nil {
		t.Fatal("ReadWord() succeeded with an unaligned address")
	}
}

func TestNewMEMAPRejectsAbsentAndNonMemoryPorts(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddAP(1, 0x00000001)
	dp := enteredDAPClient(t, target)
	if _, err := dap.NewMemAP(t.Context(), dp, 0); err == nil {
		t.Fatal("NewMemAP() accepted an absent AP")
	}
	if _, err := dap.NewMemAP(t.Context(), dp, 1); err == nil {
		t.Fatal("NewMemAP() accepted a non-MEM AP")
	}
}

func TestNilMEMAPClient(t *testing.T) {
	if _, err := dap.NewMemAP(t.Context(), nil, 0); err == nil {
		t.Fatal("NewMemAP() succeeded without a debug port")
	}
	var mem *dap.MemAP
	if _, err := mem.ReadWord(t.Context(), 0); err == nil {
		t.Fatal("ReadWord() succeeded on a nil MEM-AP")
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("nil Release(): %v", err)
	}
}

func assertAPRegister(t *testing.T, dp *dap.DebugPort, reg dap.APReg, want uint32) {
	t.Helper()
	got, err := dp.ReadAP(t.Context(), 0, reg)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("AP register %#02x = %#08x, want %#08x", reg, got, want)
	}
}

func enteredDAPClient(t *testing.T, target *sim.Target) *dap.DebugPort {
	t.Helper()
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})
	return dp
}
