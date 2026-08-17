package sim

import (
	"context"
	"testing"

	"github.com/jon/ostiole/dap"
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
