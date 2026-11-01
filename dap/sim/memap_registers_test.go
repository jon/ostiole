package sim_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestMEMAPRejectsUnmodeledBankedDataRegisters(t *testing.T) {
	for _, reg := range []uint8{0x10, 0x14, 0x18, 0x1c} {
		target := dapsim.New(0x2ba01477)
		addMEMAPFixture(t, target, 0, memAPIDR, map[uint32]uint32{0x100: 0x11223344})
		if err := target.Write(t.Context(), swdsim.Request{Addr: 8}, 0x10); err != nil {
			t.Fatal(err)
		}
		req := swdsim.Request{AP: true, Read: true, Addr: reg & 0xc}
		if _, err := target.Read(t.Context(), req); err == nil || !strings.Contains(err.Error(), "not modeled") {
			t.Errorf("BD read %#x=%v", reg, err)
		}
		req.Read = false
		if err := target.Write(t.Context(), req, 0xaabbccdd); err == nil || !strings.Contains(err.Error(), "not modeled") {
			t.Errorf("BD write %#x=%v", reg, err)
		}
		data, err := target.MEMAPBytes(apSel(0), 0x100, 4)
		if err != nil || !slices.Equal(data, []byte{0x44, 0x33, 0x22, 0x11}) {
			t.Fatalf("unsupported access changed memory: %x,%v", data, err)
		}
	}
}

func TestMEMAPFixtureRegistersIgnoreRawWrites(t *testing.T) {
	for _, cfg := range []uint32{2, 3} {
		target := dapsim.New(0x2ba01477)
		addMEMAPFixture(t, target, 0, memAPIDR, nil)
		if err := target.SetMEMAPCFG(apSel(0), cfg); err != nil {
			t.Fatal(err)
		}
		if err := target.SetMEMAPDebugBase(apSel(0), 0x12345003, 0x42); err != nil {
			t.Fatal(err)
		}
		conn := enteredConn(t, target)
		for _, reg := range []struct {
			addr uint8
			want uint32
		}{{0xf4, cfg}, {0xfc, memAPIDR}, {0xf8, 0x12345003}, {0xf0, 0x42}} {
			selectAP(t, conn, 0, 0xf)
			if err := conn.WriteAP(t.Context(), reg.addr&0xc, 0); err != nil {
				t.Fatal(err)
			}
			if got := readPosted(t, conn, 0, 0xf, reg.addr&0xc); got != reg.want {
				t.Errorf("register %#x=%#x,want%#x", reg.addr, got, reg.want)
			}
		}
	}
}

func TestGenericAPIdentityIgnoresRawWrite(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	addAPFixture(t, target, 0, 0x04770031)
	conn := enteredConn(t, target)
	selectAP(t, conn, 0, 0xf)
	if err := conn.WriteAP(t.Context(), 12, 0); err != nil {
		t.Fatal(err)
	}
	if got := readPosted(t, conn, 0, 0xf, 12); got != 0x04770031 {
		t.Fatalf("APIDR=%#x", got)
	}
}

func TestUnmappedUnalignedDRWPreservesByteMemory(t *testing.T) {
	t.Run("little", func(t *testing.T) { exerciseUnmappedUnalignedDRW(t, false) })
	t.Run("big", func(t *testing.T) { exerciseUnmappedUnalignedDRW(t, true) })
}

func exerciseUnmappedUnalignedDRW(t *testing.T, big bool) {
	t.Helper()
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, map[uint32]uint32{0x100: 0x44332211})
	if big {
		if err := target.SetMEMAPCFG(apSel(0), 1); err != nil {
			t.Fatal(err)
		}
	}
	conn := enteredConn(t, target)
	selectAP(t, conn, 0, 0)
	if err := conn.WriteAP(t.Context(), 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteAP(t.Context(), 4, 0x101); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadAP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	got, err := conn.ReadDP(t.Context(), 12)
	wantRead := uint32(0x00332200)
	if big {
		wantRead = 0x00223300
	}
	if err != nil || got&0x00ffff00 != wantRead {
		t.Fatalf("unaligned byte-run read=%#x,%v", got, err)
	}
	if err := conn.WriteAP(t.Context(), 12, 0x00556600); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	data, err := target.MEMAPBytes(apSel(0), 0x100, 4)
	wantBytes := []byte{0x11, 0x66, 0x55, 0x44}
	if big {
		wantBytes = []byte{0x11, 0x55, 0x66, 0x44}
	}
	if err != nil || !slices.Equal(data, wantBytes) {
		t.Fatalf("unaligned byte-run write=%x,%v", data, err)
	}
}

func TestFaultingDRWReadPreservesTAR(t *testing.T) {
	t.Run("device", func(t *testing.T) { exerciseFaultingReadTAR(t, false) })
	t.Run("mapping boundary", func(t *testing.T) { exerciseFaultingReadTAR(t, true) })
}

func exerciseFaultingReadTAR(t *testing.T, crossing bool) {
	t.Helper()
	device := &memoryDevice{err: dapsim.ErrBusFault}
	addr, size := uint64(0x100), uint64(4)
	if crossing {
		addr, size = 0x102, 2
	}
	target := mappedTarget(t, addr, size, device)
	conn := enteredConn(t, target)
	selectAP(t, conn, 0, 0)
	if err := conn.WriteAP(t.Context(), 0, 2|0x10); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteAP(t.Context(), 4, 0x100); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadAP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("completion=%v,wantFAULT", err)
	}
	if err := conn.WriteDP(t.Context(), 0, 0x1e); err != nil {
		t.Fatal(err)
	}
	if got := readPosted(t, conn, 0, 0, 4); got != 0x100 {
		t.Fatalf("fault incremented TAR to%#x", got)
	}
	want := 1
	if crossing {
		want = 0
	}
	if len(device.accesses) != want {
		t.Fatalf("device callbacks=%d,want%d", len(device.accesses), want)
	}
}

func TestScalarReadsMaskInactiveDRWLanes(t *testing.T) {
	for _, big := range []bool{false, true} {
		for _, size := range []dap.TransferSize{dap.Size8, dap.Size16} {
			for offset := uint64(0); offset < 4; offset += uint64(size) {
				exerciseInactiveLanes(t, big, size, offset)
			}
		}
	}
}

func exerciseInactiveLanes(t *testing.T, big bool, size dap.TransferSize, offset uint64) {
	t.Helper()
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, map[uint32]uint32{0x100: 0x44332211})
	if big {
		if err := target.SetMEMAPCFG(apSel(0), 1); err != nil {
			t.Fatal(err)
		}
	}
	dp := enteredDAP(t, target)
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mem.Release(ctx); err != nil {
			t.Error(err)
		}
	})
	data := []byte{0x11, 0x22, 0x33, 0x44}
	want := uint64(0)
	for i := uint64(0); i < uint64(size); i++ {
		shift := i * 8
		if big {
			shift = (uint64(size) - 1 - i) * 8
		}
		want |= uint64(data[offset+i]) << shift
	}
	got, err := mem.ReadScalar(t.Context(), 0x100+offset, size)
	if err != nil || got != want {
		t.Fatalf("scalar big=%t,size=%d,offset=%d: %#x,%v,want%#x", big, size, offset, got, err, want)
	}
	raw, err := dp.ReadRawAP(t.Context(), apSel(0).Address(12))
	if err != nil {
		t.Fatal(err)
	}
	shift := offset * 8
	if big {
		shift = (4 - uint64(size) - offset) * 8
	}
	mask := uint32((uint64(1)<<(uint64(size)*8))-1) << shift
	if raw&^mask != 0xa5a5a5a5&^mask {
		t.Fatalf("inactive DRW lanes=%#x,mask%#x", raw, mask)
	}
}

func TestMappedUnalignedDRWFaultsBeforeCallback(t *testing.T) {
	device := new(memoryDevice)
	target := mappedTarget(t, 0x100, 4, device)
	conn := enteredConn(t, target)
	selectAP(t, conn, 0, 0)
	if err := conn.WriteAP(t.Context(), 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteAP(t.Context(), 4, 0x101); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadAP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("unaligned device completion=%v", err)
	}
	if err := conn.WriteDP(t.Context(), 0, 0x1e); err != nil {
		t.Fatal(err)
	}
	if len(device.accesses) != 0 {
		t.Fatalf("unaligned access reached device: %v", device.accesses)
	}
}

func TestUnmappedUnalignedWordKeepsByteRun(t *testing.T) {
	for _, big := range []bool{false, true} {
		target := dapsim.New(0x2ba01477)
		addMEMAPFixture(t, target, 0, memAPIDR, map[uint32]uint32{0x100: 0x44332211, 0x104: 0x88776655})
		if big {
			if err := target.SetMEMAPCFG(apSel(0), 1); err != nil {
				t.Fatal(err)
			}
		}
		conn := enteredConn(t, target)
		selectAP(t, conn, 0, 0)
		if err := conn.WriteAP(t.Context(), 0, 2); err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteAP(t.Context(), 4, 0x101); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ReadAP(t.Context(), 12); err != nil {
			t.Fatal(err)
		}
		got, err := conn.ReadDP(t.Context(), 12)
		want := uint32(0x55443322)
		if big {
			want = 0x22334455
		}
		if err != nil || got != want {
			t.Errorf("big=%t raw unaligned word=%#x,%v want%#x", big, got, err, want)
		}
		if err := conn.WriteAP(t.Context(), 12, 0xddccbbaa); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ReadDP(t.Context(), 12); err != nil {
			t.Fatal(err)
		}
		bytes, err := target.MEMAPBytes(apSel(0), 0x100, 8)
		expected := []byte{0x11, 0xaa, 0xbb, 0xcc, 0xdd, 0x66, 0x77, 0x88}
		if big {
			expected = []byte{0x11, 0xdd, 0xcc, 0xbb, 0xaa, 0x66, 0x77, 0x88}
		}
		if err != nil || !slices.Equal(bytes, expected) {
			t.Errorf("big=%t raw unaligned write=%x,%v want%x", big, bytes, err, expected)
		}
	}
}

func TestUnmappedHalfwordsWrapDRWLanes(t *testing.T) {
	for _, big := range []bool{false, true} {
		for offset := uint64(0); offset < 4; offset++ {
			exerciseUnmappedHalfword(t, big, offset)
		}
	}
}

func exerciseUnmappedHalfword(t *testing.T, big bool, offset uint64) {
	t.Helper()
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, map[uint32]uint32{0x100: 0x44332211, 0x104: 0x88776655})
	if big {
		if err := target.SetMEMAPCFG(apSel(0), 1); err != nil {
			t.Fatal(err)
		}
	}
	conn := enteredConn(t, target)
	selectAP(t, conn, 0, 0)
	if err := conn.WriteAP(t.Context(), 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteAP(t.Context(), 4, uint32(0x100+offset)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadAP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	got, err := conn.ReadDP(t.Context(), 12)
	expectedReads := [4]uint32{0xa5a52211, 0xa53322a5, 0x4433a5a5, 0x44a5a555}
	expectedWrites := [4][2]byte{{0xdd, 0xcc}, {0xcc, 0xbb}, {0xbb, 0xaa}, {0xaa, 0xdd}}
	if big {
		expectedReads = [4]uint32{0x1122a5a5, 0xa52233a5, 0xa5a53344, 0x55a5a544}
		expectedWrites = [4][2]byte{{0xaa, 0xbb}, {0xbb, 0xcc}, {0xcc, 0xdd}, {0xdd, 0xaa}}
	}
	if err != nil || got != expectedReads[offset] {
		t.Errorf("big=%t,offset=%d raw halfword=%#x,%v,want%#x", big, offset, got, err, expectedReads[offset])
	}
	if err := conn.WriteAP(t.Context(), 12, 0xaabbccdd); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	data, err := target.MEMAPBytes(apSel(0), 0x100, 8)
	want := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	copy(want[offset:], expectedWrites[offset][:])
	if err != nil || !slices.Equal(data, want) {
		t.Errorf("big=%t,offset=%d halfword write=%x,%v,want%x", big, offset, data, err, want)
	}
	if got := readPosted(t, conn, 0, 0, 4); got != uint32(0x100+offset) {
		t.Errorf("rawhalfword changedTAR=%#x", got)
	}
}

func TestMappedCrossWordHalfwordFaultsBeforeCallbacks(t *testing.T) {
	for _, big := range []bool{false, true} {
		for _, write := range []bool{false, true} {
			exerciseMappedCrossWordHalfword(t, big, write)
		}
	}
}

func exerciseMappedCrossWordHalfword(t *testing.T, big, write bool) {
	t.Helper()
	device := &memoryDevice{data: map[uint64]byte{0x103: 0x44, 0x104: 0x55}}
	target := mappedTarget(t, 0x100, 8, device)
	if big {
		if err := target.SetMEMAPCFG(apSel(0), 1); err != nil {
			t.Fatal(err)
		}
	}
	conn := enteredConn(t, target)
	selectAP(t, conn, 0, 0)
	if err := conn.WriteAP(t.Context(), 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteAP(t.Context(), 4, 0x103); err != nil {
		t.Fatal(err)
	}
	if write {
		if err := conn.WriteAP(t.Context(), 12, 0xaabbccdd); err != nil {
			t.Fatal(err)
		}
	} else if _, err := conn.ReadAP(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReadDP(t.Context(), 12); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("mapped cross-word halfword big=%t,write=%t completion=%v,wantFAULT", big, write, err)
	}
	if err := conn.WriteDP(t.Context(), 0, 0x1e); err != nil {
		t.Fatal(err)
	}
	if got := readPosted(t, conn, 0, 0, 4); got != 0x103 {
		t.Errorf("mapped fault changed TAR to%#x", got)
	}
	if len(device.accesses) != 0 || device.data[0x103] != 0x44 || device.data[0x104] != 0x55 {
		t.Fatalf("unaligned mapped access reached device: %v,%v", device.accesses, device.data)
	}
}
