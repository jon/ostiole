package sim

import (
	"context"
	"testing"

	"github.com/jon/ostiole/swd/sim"
)

func TestTargetImplementsSWDSimulationTarget(t *testing.T) {
	var _ sim.Target = New(0x2ba01477)
}

func TestTargetReportsIdentityAndPowerState(t *testing.T) {
	target := New(0x2ba01477)
	ctx := context.Background()

	value, err := target.Read(ctx, sim.Request{Read: true})
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x2ba01477 {
		t.Fatalf("DPIDR = %#08x, want 0x2ba01477", value)
	}

	const requests = uint32(1<<28 | 1<<30)
	if err := target.Write(ctx, sim.Request{Addr: 4}, requests); err != nil {
		t.Fatal(err)
	}
	value, err = target.Read(ctx, sim.Request{Read: true, Addr: 4})
	if err != nil {
		t.Fatal(err)
	}
	want := requests | 1<<29 | 1<<31
	if value != want {
		t.Fatalf("CTRL/STAT = %#08x, want %#08x", value, want)
	}
}

func TestTargetModelsBankedDPRegisters(t *testing.T) {
	target := New(0x2ba01477)
	ctx := context.Background()
	const dlcr = uint32(0xa5a50000)
	if err := target.SetBankedDPRegister(1, dlcr); err != nil {
		t.Fatal(err)
	}
	if err := target.Write(ctx, sim.Request{Addr: 0x08}, 1); err != nil {
		t.Fatal(err)
	}
	value, err := target.Read(ctx, sim.Request{Read: true, Addr: 0x04})
	if err != nil {
		t.Fatal(err)
	}
	if value != dlcr {
		t.Fatalf("DLCR = %#08x, want %#08x", value, dlcr)
	}

	const changed = uint32(0x5a5a0000)
	if err := target.Write(ctx, sim.Request{Addr: 0x04}, changed); err != nil {
		t.Fatal(err)
	}
	value, err = target.Read(ctx, sim.Request{Read: true, Addr: 0x04})
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

func TestTargetRejectsInvalidBankedDPRegister(t *testing.T) {
	target := New(0x2ba01477)
	for _, bank := range []uint8{0, 16} {
		if err := target.SetBankedDPRegister(bank, 0); err == nil {
			t.Fatalf("SetBankedDPRegister(%d) succeeded", bank)
		}
	}
	if err := target.SetBankedDPRegister(1, 1<<8); err == nil {
		t.Fatal("SetBankedDPRegister() accepted unsupported turnaround")
	}
	if err := target.Write(t.Context(), sim.Request{Addr: 0x08}, 1); err != nil {
		t.Fatal(err)
	}
	if err := target.Write(t.Context(), sim.Request{Addr: 0x04}, 1<<8); err == nil {
		t.Fatal("DLCR write accepted unsupported turnaround")
	}
}

func TestAbortClearsStickyState(t *testing.T) {
	target := New(0x2ba01477)
	target.ctrlStat = stickyCompare | stickyError | writeDataError | stickyOverrun

	err := target.Write(context.Background(), sim.Request{},
		clearStickyCompare|clearStickyError|clearWriteDataError|clearStickyOverrun)
	if err != nil {
		t.Fatal(err)
	}
	if target.ctrlStat != 0 {
		t.Fatalf("CTRL/STAT = %#08x, want 0", target.ctrlStat)
	}
}

func TestTargetPostsZeroForAnAbsentAccessPort(t *testing.T) {
	target := New(0x2ba01477)
	value, err := target.Read(context.Background(), sim.Request{AP: true, Read: true})
	if err != nil {
		t.Fatal(err)
	}
	if value != 0 || target.rdbuff != 0 {
		t.Fatalf("absent AP posted %#08x and buffered %#08x", value, target.rdbuff)
	}
}
