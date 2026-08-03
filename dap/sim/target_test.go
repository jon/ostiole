package sim

import (
	"context"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

func TestTargetImplementsSWDSimulationTarget(t *testing.T) {
	var _ sim.Target = New(0x2ba01477)
}

func TestTargetReportsIdentityAndPowerState(t *testing.T) {
	target := New(0x2ba01477)
	ctx := context.Background()

	value, err := target.Read(ctx, swd.Request{Read: true})
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x2ba01477 {
		t.Fatalf("DPIDR = %#08x, want 0x2ba01477", value)
	}

	const requests = uint32(1<<28 | 1<<30)
	if err := target.Write(ctx, swd.Request{Addr: 4}, requests); err != nil {
		t.Fatal(err)
	}
	value, err = target.Read(ctx, swd.Request{Read: true, Addr: 4})
	if err != nil {
		t.Fatal(err)
	}
	want := requests | 1<<29 | 1<<31
	if value != want {
		t.Fatalf("CTRL/STAT = %#08x, want %#08x", value, want)
	}
}

func TestAbortClearsStickyState(t *testing.T) {
	target := New(0x2ba01477)
	target.ctrlStat = stickyCompare | stickyError | writeDataError | stickyOverrun

	err := target.Write(context.Background(), swd.Request{},
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
	value, err := target.Read(context.Background(), swd.Request{AP: true, Read: true})
	if err != nil {
		t.Fatal(err)
	}
	if value != 0 || target.rdbuff != 0 {
		t.Fatalf("absent AP posted %#08x and buffered %#08x", value, target.rdbuff)
	}
}
