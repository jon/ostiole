package sim

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func readDPRequest(addr uint8) swdsim.Request {
	return swdsim.Request{Read: true, Addr: addr}
}

func writeDPRequest(addr uint8) swdsim.Request {
	return swdsim.Request{Addr: addr}
}

func readAPRequest(addr uint8) swdsim.Request {
	return swdsim.Request{AP: true, Read: true, Addr: addr}
}

func TestTargetImplementsSWDSimulationTarget(t *testing.T) {
	var _ swdsim.Target = New(0x2ba01477)
}

func TestTargetReportsIdentityAndPowerState(t *testing.T) {
	target := New(0x2ba01477)
	ctx := context.Background()

	value, err := target.Read(ctx, readDPRequest(0x00))
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x2ba01477 {
		t.Fatalf("DPIDR = %#08x, want 0x2ba01477", value)
	}

	const requests = uint32(1<<28 | 1<<30)
	if err := target.Write(ctx, writeDPRequest(0x04), requests); err != nil {
		t.Fatal(err)
	}
	value, err = target.Read(ctx, readDPRequest(0x04))
	if err != nil {
		t.Fatal(err)
	}
	want := requests | 1<<29 | 1<<31
	if value != want {
		t.Fatalf("CTRL/STAT = %#08x, want %#08x", value, want)
	}
}

func TestTargetRejectsInvalidRequestDirections(t *testing.T) {
	target := New(0x2ba01477)
	if _, err := target.Read(t.Context(), writeDPRequest(0x00)); err == nil {
		t.Fatal("Read() accepted a write request")
	}
	if err := target.Write(t.Context(), readDPRequest(0x00), 0); err == nil {
		t.Fatal("Write() accepted a read request")
	}
}

func TestTargetRejectsInvalidRequestAddress(t *testing.T) {
	target := New(0x2ba01477)
	if _, err := target.Read(t.Context(), readDPRequest(0x02)); err == nil {
		t.Fatal("Read() accepted address 0x02")
	}
	if err := target.Write(t.Context(), writeDPRequest(0x10), 0); err == nil {
		t.Fatal("Write() accepted address 0x10")
	}
}

func TestTargetSetsMEMAPCFG(t *testing.T) {
	target := New(0x2ba01477)
	sel := dap.NewAPSel(0)
	if err := target.AddMEMAP(sel, 0x00010001, nil); err != nil {
		t.Fatal(err)
	}
	const cfg = uint32(0x07)
	if err := target.SetMEMAPCFG(sel, cfg); err != nil {
		t.Fatal(err)
	}
	if got := target.aps[sel].regs[0xf4]; got != cfg {
		t.Fatalf("CFG = %#08x, want %#08x", got, cfg)
	}
	if err := target.SetMEMAPCFG(dap.APSel{}, cfg); err == nil {
		t.Fatal("SetMEMAPCFG() accepted a zero APSel")
	}
	if err := (*Target)(nil).SetMEMAPCFG(sel, cfg); err == nil {
		t.Fatal("SetMEMAPCFG() succeeded on a nil target")
	}
}

func TestTargetSnapshotsSize64ReadData(t *testing.T) {
	target := New(0x2ba01477)
	sel := dap.NewAPSel(0)
	if err := target.AddMEMAP(sel, 0x00010001, nil); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPCFG(sel, 1<<2); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPBytes(sel, 0x100, []byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}); err != nil {
		t.Fatal(err)
	}
	ap := target.aps[sel]
	ap.regs[0] = 3
	ap.regs[4] = 0x100
	low, err := ap.readDRW()
	if err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPBytes(sel, 0x104, []byte{0xaa, 0xbb, 0xcc, 0xdd}); err != nil {
		t.Fatal(err)
	}
	high, err := ap.readDRW()
	if err != nil {
		t.Fatal(err)
	}
	if low != 0x55667788 || high != 0x11223344 {
		t.Fatalf("Size64 DRW words = %#08x, %#08x; want 0x55667788, 0x11223344", low, high)
	}
}

func TestTargetRequiresCSWToTerminateIncompleteSize64Transfer(t *testing.T) {
	target := New(0x2ba01477)
	sel := dap.NewAPSel(0)
	if err := target.AddMEMAP(sel, 0x00010001, nil); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPCFG(sel, 1<<2); err != nil {
		t.Fatal(err)
	}
	ap := target.aps[sel]
	ap.regs[0] = 3
	if _, err := ap.readDRW(); err != nil {
		t.Fatal(err)
	}
	if err := target.Write(t.Context(), swdsim.Request{AP: true, Addr: 0x04}, 0x100); err == nil {
		t.Fatal("TAR write succeeded during incomplete Size64 read")
	}
	if err := ap.writeDRW(0); err == nil {
		t.Fatal("DRW write succeeded during incomplete Size64 read")
	}
	if err := target.Write(t.Context(), swdsim.Request{AP: true, Addr: 0x00}, 3); err != nil {
		t.Fatal(err)
	}
	if err := ap.writeDRW(0x55667788); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Read(t.Context(), swdsim.Request{AP: true, Read: true, Addr: 0x04}); err == nil {
		t.Fatal("TAR read succeeded during incomplete Size64 write")
	}
	if _, err := ap.readDRW(); err == nil {
		t.Fatal("DRW read succeeded during incomplete Size64 write")
	}
	if _, err := target.Read(t.Context(), swdsim.Request{AP: true, Read: true, Addr: 0x00}); err != nil {
		t.Fatal(err)
	}
	if err := target.Write(t.Context(), swdsim.Request{AP: true, Addr: 0x04}, 0x100); err != nil {
		t.Fatalf("TAR write after terminating Size64 through CSW: %v", err)
	}
}

func TestTargetUsesTARHIOnlyWithLargeAddressExtension(t *testing.T) {
	target := New(0x2ba01477)
	sel := dap.NewAPSel(0)
	if err := target.AddMEMAP(sel, 0x00010001, nil); err != nil {
		t.Fatal(err)
	}
	ap := target.aps[sel]
	ap.regs[4] = 0x100
	if err := target.Write(t.Context(), swdsim.Request{AP: true, Addr: 0x08}, 1); err != nil {
		t.Fatal(err)
	}
	if ap.regs[8] != 0 || ap.targetAddress() != 0x100 {
		t.Fatalf("address without CFG.LA = %#x with TARHI %#x; want 0x100 with TARHI zero", ap.targetAddress(), ap.regs[8])
	}
	if _, err := target.Read(t.Context(), swdsim.Request{AP: true, Read: true, Addr: 0x08}); err != nil {
		t.Fatal(err)
	}
	value, err := target.Read(t.Context(), readDPRequest(0x0c))
	if err != nil || value != 0 {
		t.Fatalf("TARHI without CFG.LA = %#08x, %v; want zero, nil", value, err)
	}
	if err := target.SetMEMAPCFG(sel, 1<<1); err != nil {
		t.Fatal(err)
	}
	if err := target.Write(t.Context(), swdsim.Request{AP: true, Addr: 0x08}, 1); err != nil {
		t.Fatal(err)
	}
	if ap.targetAddress() != 0x100000100 {
		t.Fatalf("address with CFG.LA = %#x, want 0x100000100", ap.targetAddress())
	}
}

func TestTargetModelsLogicalDPRegisters(t *testing.T) {
	target := New(0x2ba01477)
	ctx := context.Background()
	const dlcr = uint32(0xa5a50000)
	if err := target.SetDPRegister(dap.DLCR, dlcr); err != nil {
		t.Fatal(err)
	}
	if err := target.Write(ctx, writeDPRequest(0x08), 1); err != nil {
		t.Fatal(err)
	}
	value, err := target.Read(ctx, readDPRequest(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if value != dlcr {
		t.Fatalf("DLCR = %#08x, want %#08x", value, dlcr)
	}

	const changed = uint32(0x5a5a0000)
	if err := target.Write(ctx, writeDPRequest(0x04), changed); err != nil {
		t.Fatal(err)
	}
	value, err = target.Read(ctx, readDPRequest(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if value != changed {
		t.Fatalf("DLCR after write = %#08x, want %#08x", value, changed)
	}
	if target.ctrlStat != 0 {
		t.Fatalf("CTRL/STAT after DLCR write = %#08x, want 0", target.ctrlStat)
	}
}

func TestTargetRejectsInvalidDPRegisterFixture(t *testing.T) {
	target := New(0x2ba01477)
	for _, reg := range []dap.DPRegister{0, dap.CTRLSTAT, dap.RDBUFF} {
		if err := target.SetDPRegister(reg, 0); err == nil {
			t.Fatalf("SetDPRegister(%v) succeeded", reg)
		}
	}
	if err := target.SetDPRegister(dap.DLCR, 1<<8); err == nil {
		t.Fatal("SetDPRegister(DLCR) accepted unsupported turnaround")
	}
	if err := target.SetDPRegister(dap.TARGETID, 1); err != nil {
		t.Fatal(err)
	}
	if err := target.SetDPRegister(dap.TARGETID, 2); err == nil {
		t.Fatal("SetDPRegister(TARGETID) replaced an existing fixture")
	}
	if err := target.Write(t.Context(), writeDPRequest(0x08), 1); err != nil {
		t.Fatal(err)
	}
	if err := target.Write(t.Context(), writeDPRequest(0x04), 1<<8); err == nil {
		t.Fatal("DLCR write accepted unsupported turnaround")
	}
}

func TestTargetModelsRESEND(t *testing.T) {
	target := New(0x2ba01477)
	target.rdbuff = 0x12345678
	value, err := target.Read(t.Context(), readDPRequest(0x08))
	if err != nil {
		t.Fatal(err)
	}
	if value != target.rdbuff {
		t.Fatalf("RESEND = %#08x, want %#08x", value, target.rdbuff)
	}
}

func TestAbortClearsStickyState(t *testing.T) {
	target := New(0x2ba01477)
	target.ctrlStat = stickyCompare | stickyError | writeDataError | stickyOverrun

	err := target.Write(context.Background(), writeDPRequest(0x00), clearStickyCompare|clearStickyError|clearWriteDataError|clearStickyOverrun)
	if err != nil {
		t.Fatal(err)
	}
	if target.ctrlStat != 0 {
		t.Fatalf("CTRL/STAT = %#08x, want 0", target.ctrlStat)
	}
}

func TestTargetModelsOverrunDetectionState(t *testing.T) {
	target := New(0x2ba01477)
	target.SetOverrunDetect(true)
	if !target.OverrunDetectEnabled() {
		t.Fatal("ORUNDETECT was not enabled")
	}
	target.ObserveResponse(swd.ErrWait)
	state, err := target.Read(t.Context(), readDPRequest(0x04))
	if err != nil {
		t.Fatal(err)
	}
	if state&(overrunDetect|stickyOverrun) != overrunDetect|stickyOverrun {
		t.Fatalf("CTRL/STAT = %#08x, want ORUNDETECT and STICKYORUN", state)
	}
	if err := target.Write(t.Context(), writeDPRequest(0x00), clearStickyOverrun); err != nil {
		t.Fatal(err)
	}
	if !target.OverrunDetectEnabled() {
		t.Fatal("ORUNERRCLR changed ORUNDETECT")
	}
}

func TestTargetModelsLineResetStateDuringConnection(t *testing.T) {
	target := New(0x2ba01477)
	if err := target.SetDPRegister(dap.DLCR, 0xa5a50000); err != nil {
		t.Fatal(err)
	}
	target.SetOverrunDetect(true)
	conn := swd.New(swdsim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	state, err := conn.ReadDP(t.Context(), 0x04)
	if err != nil {
		t.Fatalf("ReadDP(CTRL/STAT): %v", err)
	}
	if state&stickyOverrun != 0 {
		t.Fatalf("CTRL/STAT after Connect = %#08x, want reset STICKYORUN cleared", state)
	}
	if err := conn.WriteDP(t.Context(), 0x08, 1); err != nil {
		t.Fatalf("WriteDP(SELECT): %v", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x0c); err != nil {
		t.Fatalf("ReadDP(RDBUFF): %v", err)
	}
	if value, err := conn.ReadDP(t.Context(), 0x04); err != nil || value != 0 {
		t.Fatalf("ReadDP(DLCR) = %#08x, %v; want reset value", value, err)
	}
	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("Release(): %v", err)
	}
}

func TestTargetFaultsOrdinaryRequestsWhileStickyStateIsSet(t *testing.T) {
	target := New(0x2ba01477)
	target.SetOverrunDetect(true)
	target.ObserveResponse(swd.ErrWait)
	if err := target.Acknowledge(t.Context(), readAPRequest(0x00)); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("AP acknowledgement = %v, want FAULT", err)
	}
	for _, req := range []swdsim.Request{readDPRequest(0x00), readDPRequest(0x04), writeDPRequest(0x00)} {
		if err := target.Acknowledge(t.Context(), req); err != nil {
			t.Fatalf("exempt request %v acknowledgement: %v", req, err)
		}
	}
}

func TestTargetPostsZeroForAnAbsentAccessPort(t *testing.T) {
	target := New(0x2ba01477)
	value, err := target.Read(context.Background(), readAPRequest(0x00))
	if err != nil {
		t.Fatal(err)
	}
	if value != 0 || target.rdbuff != 0 {
		t.Fatalf("absent AP posted %#08x and buffered %#08x", value, target.rdbuff)
	}
}

func TestTargetRejectsInvalidAccessPortFixtures(t *testing.T) {
	target := New(0x2ba01477)
	var zero dap.APSel
	if err := target.AddAP(zero, 0x04770031); err == nil {
		t.Fatal("AddAP() accepted a zero APSel")
	}
	if err := target.AddMEMAP(zero, 0x00010001, nil); err == nil {
		t.Fatal("AddMEMAP() accepted a zero APSel")
	}
	if err := target.AddAP(dap.NewAPSel(0), 0); err == nil {
		t.Fatal("AddAP() accepted a zero APIDR")
	}
	if err := target.AddAP(dap.NewAPSel(0), 0x04770031); err != nil {
		t.Fatal(err)
	}
	if err := target.AddAP(dap.NewAPSel(0), 0x04770032); err == nil {
		t.Fatal("AddAP() replaced an existing AP")
	}
	if err := target.AddMEMAP(dap.NewAPSel(1), 0x00000001, nil); err == nil {
		t.Fatal("AddMEMAP() accepted a non-MEM-AP identity")
	}
	if err := target.AddMEMAP(dap.NewAPSel(1), 0x00010001, map[uint32]uint32{2: 1}); err == nil {
		t.Fatal("AddMEMAP() accepted an unaligned target-word address")
	}
	if err := target.AddMEMAP(dap.NewAPSel(1), 0x00010001, map[uint32]uint32{0: 1}); err != nil {
		t.Fatal(err)
	}
	if err := target.AddMEMAP(dap.NewAPSel(1), 0x00010001, nil); err == nil {
		t.Fatal("AddMEMAP() replaced an existing AP")
	}
}

func TestNilTargetRejectsAccessPortFixtures(t *testing.T) {
	var target *Target
	if err := target.AddAP(dap.NewAPSel(0), 1); err == nil {
		t.Fatal("AddAP() succeeded on a nil target")
	}
	if err := target.AddMEMAP(dap.NewAPSel(0), 0x00010001, nil); err == nil {
		t.Fatal("AddMEMAP() succeeded on a nil target")
	}
}
