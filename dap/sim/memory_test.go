package sim_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

const (
	memAPIDR = uint32(0x00010001)
	wordAddr = uint32(0xe000ed00)
	wordData = uint32(0x410cc200)
)

func TestMEMAPReadsConfiguredTargetWord(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, map[uint32]uint32{wordAddr: wordData})
	dp := enteredDAP(t, target)

	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 2); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x04), wordAddr); err != nil {
		t.Fatal(err)
	}
	value, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x0c))
	if err != nil {
		t.Fatal(err)
	}
	if value != wordData {
		t.Fatalf("DRW = %#08x, want %#08x", value, wordData)
	}
}

func TestMEMAPCopiesTargetWords(t *testing.T) {
	words := map[uint32]uint32{wordAddr: wordData}
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, words)
	words[wordAddr] = 0
	dp := enteredDAP(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 2); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x04), wordAddr); err != nil {
		t.Fatal(err)
	}
	value, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x0c))
	if err != nil {
		t.Fatal(err)
	}
	if value != wordData {
		t.Fatalf("DRW = %#08x after source mutation, want %#08x", value, wordData)
	}
}

func TestMEMAPRejectsUnsupportedCSW(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, map[uint32]uint32{wordAddr: wordData})
	dp := enteredDAP(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x04), wordAddr); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 2|0x20); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x0c)); err == nil {
		t.Fatal("DRW read succeeded with unsupported AddrInc")
	}
}

func TestMEMAPModelsTargetWrites(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, nil)
	dp := enteredDAP(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 2); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x04), wordAddr); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x0c), wordData); err != nil {
		t.Fatal(err)
	}
	data, err := target.MEMAPBytes(apSel(0), uint64(wordAddr), 4)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x00, 0xc2, 0x0c, 0x41}; !slices.Equal(data, want) {
		t.Fatalf("target bytes = % x, want % x", data, want)
	}
}

func enteredDAP(t *testing.T, target *dapsim.Target) *dap.DebugPort {
	t.Helper()
	conn := swd.New(swdsim.New(target))
	dp := dap.NewDebugPort(conn)
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
