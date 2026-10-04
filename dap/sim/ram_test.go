package sim_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
)

func TestRAMSharesBytesThroughTwoAPsAndKeepsDevicesPrivate(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	ram := new(dapsim.RAM)
	for i := range uint8(2) {
		addMEMAPFixture(t, target, i, memAPIDR, nil)
		if err := target.MapMEMAPDevice(apSel(i), 0x100, 0x400, ram); err != nil {
			t.Fatal(err)
		}
		if err := target.MapMEMAPDevice(apSel(i), 0xe000ed00, 4, new(memoryDevice)); err != nil {
			t.Fatal(err)
		}
	}
	dp := enteredDAP(t, target)
	var memories [2]*dap.MemAP
	for i := range memories {
		m, err := dap.OpenMemAP(t.Context(), dp, apSel(uint8(i)))
		if err != nil {
			t.Fatal(err)
		}
		memories[i] = m
		t.Cleanup(func() {
			if err := m.Release(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	if err := memories[0].WriteWord(t.Context(), 0x100, 0x12345678); err != nil {
		t.Fatal(err)
	}
	if got, err := memories[1].ReadWord(t.Context(), 0x100); err != nil || got != 0x12345678 {
		t.Fatalf("shared RAM read = %#x, %v", got, err)
	}
	if err := memories[1].WriteScalar(t.Context(), 0x101, dap.Size8, 0xaa); err != nil {
		t.Fatal(err)
	}
	if got, err := memories[0].ReadWord(t.Context(), 0x100); err != nil || got != 0x1234aa78 {
		t.Fatalf("peer narrow write = %#x, %v", got, err)
	}
	if err := memories[0].WriteWord(t.Context(), 0xe000ed00, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := memories[1].ReadWord(t.Context(), 0xe000ed00); err != nil || got != 0 {
		t.Fatalf("core-private device leaked to peer: %d, %v", got, err)
	}
	data, err := ram.Bytes(0x100, 4)
	if err != nil || !slices.Equal(data, []byte{0x78, 0xaa, 0x34, 0x12}) {
		t.Fatalf("RAM snapshot = %v, %v", data, err)
	}
}

func TestRAMFixturesCopyAndRejectOverflow(t *testing.T) {
	ram := new(dapsim.RAM)
	data := []byte{1, 2, 3, 4}
	if err := ram.SetBytes(0x100, data); err != nil {
		t.Fatal(err)
	}
	data[0] = 9
	got, err := ram.Bytes(0x100, 4)
	if err != nil || !slices.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("RAM fixture = %v, %v", got, err)
	}
	got[0] = 9
	again, err := ram.Bytes(0x100, 4)
	if err != nil || again[0] != 1 {
		t.Fatalf("snapshot aliases RAM: %v, %v", again, err)
	}
	if err := ram.SetBytes(math.MaxUint64, []byte{5, 6}); err == nil {
		t.Fatal("overflowing fixture write succeeded")
	}
	if _, err := ram.Bytes(math.MaxUint64, 2); err == nil {
		t.Fatal("overflowing snapshot succeeded")
	}
	if _, err := ram.Bytes(0, -1); err == nil {
		t.Fatal("negative snapshot succeeded")
	}
	if err := ram.SetBytes(math.MaxUint64, []byte{5}); err != nil {
		t.Fatal(err)
	}
	if got, err := ram.Bytes(math.MaxUint64, 1); err != nil || got[0] != 5 {
		t.Fatalf("last byte = %v, %v", got, err)
	}
}

func TestRAMDeviceRejectsCancellationAndInvalidAccess(t *testing.T) {
	ram := new(dapsim.RAM)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ram.Write(ctx, 0x100, []byte{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write = %v", err)
	}
	if err := ram.Read(ctx, 0x100, []byte{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
	for _, test := range []struct {
		addr uint64
		size int
	}{{0, 0}, {0, 3}, {1, 2}, {math.MaxUint64, 4}} {
		if err := ram.Write(t.Context(), test.addr, make([]byte, test.size)); !errors.Is(err, dapsim.ErrBusFault) {
			t.Fatalf("invalid write %+v = %v", test, err)
		}
		if err := ram.Read(t.Context(), test.addr, make([]byte, test.size)); !errors.Is(err, dapsim.ErrBusFault) {
			t.Fatalf("invalid read %+v = %v", test, err)
		}
	}
	if got, err := ram.Bytes(0x100, 1); err != nil || got[0] != 0 {
		t.Fatalf("canceled write changed RAM: %v, %v", got, err)
	}
	if err := (*dapsim.RAM)(nil).SetBytes(0, []byte{1}); err == nil {
		t.Fatal("nil RAM fixture succeeded")
	}
	if _, err := (*dapsim.RAM)(nil).Bytes(0, 1); err == nil {
		t.Fatal("nil RAM snapshot succeeded")
	}
	if err := (*dapsim.RAM)(nil).Read(t.Context(), 0, []byte{1}); err == nil {
		t.Fatal("nil RAM read succeeded")
	}
	if err := (*dapsim.RAM)(nil).Write(t.Context(), 0, []byte{1}); err == nil {
		t.Fatal("nil RAM write succeeded")
	}
}

func TestRAMSharedADIv6ViewsUseTheirOwnByteOrder(t *testing.T) {
	target := dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, 32); err != nil {
		t.Fatal(err)
	}
	ram := new(dapsim.RAM)
	var selectors [2]dap.APSel
	for i := range selectors {
		sel, err := dap.APAt(uint64(0x2000 + i*0x2000))
		if err != nil {
			t.Fatal(err)
		}
		selectors[i] = sel
		if err := target.AddMEMAP(sel, memAPIDR, nil); err != nil {
			t.Fatal(err)
		}
		if err := target.SetMEMAPCFG(sel, uint32(i)); err != nil {
			t.Fatal(err)
		}
		if err := target.MapMEMAPDevice(sel, 0x100, 4, ram); err != nil {
			t.Fatal(err)
		}
	}
	dp := enteredDAP(t, target)
	var memories [2]*dap.MemAP
	for i, sel := range selectors {
		m, err := dap.OpenMemAP(t.Context(), dp, sel)
		if err != nil {
			t.Fatal(err)
		}
		memories[i] = m
		t.Cleanup(func() {
			if err := m.Release(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	if err := memories[0].WriteWord(t.Context(), 0x100, 0x12345678); err != nil {
		t.Fatal(err)
	}
	if got, err := memories[1].ReadWord(t.Context(), 0x100); err != nil || got != 0x78563412 {
		t.Fatalf("big-endian view = %#x, %v", got, err)
	}
	if err := memories[1].WriteScalar(t.Context(), 0x102, dap.Size16, 0xabcd); err != nil {
		t.Fatal(err)
	}
	if got, err := memories[0].ReadWord(t.Context(), 0x100); err != nil || got != 0xcdab5678 {
		t.Fatalf("little-endian peer = %#x, %v", got, err)
	}
}
