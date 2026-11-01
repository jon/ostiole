package dap_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestMEMAPSize64AddressFailureDoesNotRequireDataCleanup(t *testing.T) {
	for _, write := range []bool{false, true} {
		for _, reg := range []uint8{4, 8} {
			for _, canceled := range []bool{false, true} {
				name := map[bool]string{false: "read", true: "write"}[write] + map[uint8]string{4: "/TAR", 8: "/TARHI"}[reg] + map[bool]string{false: "/FAULT", true: "/canceled"}[canceled]
				t.Run(name, func(t *testing.T) { testSize64AddressFailure(t, write, reg, canceled) })
			}
		}
	}
}

func testSize64AddressFailure(t *testing.T, write bool, reg uint8, canceled bool) {
	t.Helper()
	target := &cancelAPTarget{waitTarget: newWaitTarget(), req: apWrite(reg), afterBarrier: true}
	addMEMAP(t, target, 0, 0x00010001, nil)
	if err := target.SetMEMAPCFG(apSel(0), 6); err != nil {
		t.Fatal(err)
	}
	wire := &packedTxnWire{inner: swdsim.New(target), limit: 54}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	before := len(target.requests)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wantErr := dap.ErrFault
	if canceled {
		target.cancel = cancel
		wantErr = context.Canceled
	} else {
		target.armFault(apWrite(reg))
	}
	if write {
		err = mem.WriteScalar(ctx, 0x2_00000100, dap.Size64, 0x1122334455667788)
	} else {
		_, err = mem.ReadScalar(ctx, 0x2_00000100, dap.Size64)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("address failure = %v, want %v", err, wantErr)
	}
	if n := countRequests(target.requests[before:], apRead(12)) + countRequests(target.requests[before:], apWrite(12)); n != 0 {
		t.Fatalf("address-only failure sent %d DRW requests", n)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err != nil {
		t.Errorf("address-only failure blocked debug port: %v", err)
	}
	if _, err := mem.ReadWord(t.Context(), 0x200); err != nil {
		t.Errorf("address-only failure invalidated MEM-AP: %v", err)
	}
	if err := mem.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMEMAPBlockChangesAddressWindowIndependently(t *testing.T) {
	for _, link := range []string{"SWD", "JTAG"} {
		for _, write := range []bool{false, true} {
			t.Run(link+map[bool]string{false: "/read", true: "/write"}[write], func(t *testing.T) {
				testBlockAddressWindow(t, link, write)
			})
		}
	}
}

func testBlockAddressWindow(t *testing.T, link string, write bool) {
	t.Helper()
	dp, mem, target := largeCleanupMEMAP(t, link)
	want := []byte{0x12, 0x34, 0x56, 0x78, 0x98, 0x76, 0x54, 0x32}
	other := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22}
	for addr, data := range map[uint64][]byte{0x2_00000100: want, 0x3_00000100: other} {
		if err := target.SetMEMAPBytes(apSel(0), addr, data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mem.ReadScalar(t.Context(), 0x3_00000200, dap.Size32); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(want))
	var n int
	var err error
	if write {
		want = []byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}
		n, err = mem.WriteBlock(t.Context(), 0x2_00000100, want)
	} else {
		n, err = mem.ReadBlock(t.Context(), 0x2_00000100, data)
	}
	if err != nil || n != len(want) {
		t.Fatalf("block transfer = %d, %v", n, err)
	}
	if !write && !slices.Equal(data, want) {
		t.Errorf("block read = % x, want % x", data, want)
	}
	for addr, expected := range map[uint64][]byte{0x2_00000100: want, 0x3_00000100: other} {
		got, err := target.MEMAPBytes(apSel(0), addr, len(expected))
		if err != nil || !slices.Equal(got, expected) {
			t.Errorf("independent bytes at %#x = % x, %v; want % x", addr, got, err, expected)
		}
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMEMAPBlockOnlyRestoresAllRegisters(t *testing.T) {
	for _, link := range []string{"SWD", "JTAG"} {
		for _, write := range []bool{false, true} {
			t.Run(link+map[bool]string{false: "/read", true: "/write"}[write], func(t *testing.T) {
				dp, mem, _ := largeCleanupMEMAP(t, link)
				data := []byte{1, 2, 3, 4, 5, 6, 7, 8}
				var err error
				if write {
					_, err = mem.WriteBlock(t.Context(), 0x2_00000100, data)
				} else {
					_, err = mem.ReadBlock(t.Context(), 0x2_00000100, data)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := mem.Release(t.Context()); err != nil {
					t.Fatal(err)
				}
				for reg, want := range map[uint16]uint32{0: 0xa5000051, 4: 0x7788, 8: 7} {
					got, err := dp.ReadRawAP(t.Context(), apSel(0).Address(reg))
					if err != nil || got != want {
						t.Errorf("restored register %#x = %#x, %v; want %#x", reg, got, err, want)
					}
				}
				if err := dp.Release(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func largeCleanupMEMAP(t *testing.T, link string) (*dap.DebugPort, *dap.MemAP, *sim.Target) {
	t.Helper()
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, nil)
	if err := target.SetMEMAPCFG(apSel(0), 6); err != nil {
		t.Fatal(err)
	}
	port := dap.SWDP(swd.New(swdsim.New(target)))
	if link == "JTAG" {
		model, _, chain := jtagModelChain(t, 4, 0)
		shareAPModel(t, model, target)
		port = dap.JTAGDP(chain, 0)
	}
	dp := dap.NewDebugPort(port)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	for reg, value := range map[uint16]uint32{0: 0xa5000051, 4: 0x7788, 8: 7} {
		if err := dp.WriteRawAP(t.Context(), apSel(0).Address(reg), value); err != nil {
			t.Fatal(err)
		}
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	return dp, mem, target
}
