package sim_test

import (
	"errors"
	"testing"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/target/cortexm/sim"
)

func TestKeyedControlAndReasons(t *testing.T) {
	for _, p := range []sim.Profile{sim.M0, sim.M33} {
		t.Run(string(rune('0'+p)), func(t *testing.T) { checkKeyedControl(t, p) })
	}
}

func checkKeyedControl(t *testing.T, p sim.Profile) {
	t.Helper()
	initial := sim.Snapshot{}
	if p == sim.M33 {
		initial.DHCSR = secure
	}
	c := newCore(t, p, initial)
	if err := write(t, c, dhcsr, 3); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot() != initial {
		t.Fatal("unkeyed write changed state")
	}
	if err := write(t, c, dhcsr, key|enable); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted != 0 {
		t.Fatal("enabling debug halted core")
	}
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&(enable|halt|halted) != enable|halt|halted || read(t, c, dfsr) != 1 {
		t.Fatal("halt not completed")
	}
	if err := write(t, c, dfsr, 1); err != nil || read(t, c, dfsr) != 0 {
		t.Fatal("DFSR W1C failed")
	}
	if err := write(t, c, dhcsr, key|enable); err != nil {
		t.Fatal(err)
	}
	got := c.Snapshot().DHCSR
	if got&(halt|halted) != 0 {
		t.Fatal("resume did not clear Debug state")
	}
	wantRestart := uint32(0)
	if p == sim.M33 {
		wantRestart = restart
	}
	if got&restart != wantRestart {
		t.Fatalf("restart status %#x want %#x", got&restart, wantRestart)
	}
	if err := write(t, c, dhcsr, key); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&enable != 0 {
		t.Fatal("debug still enabled")
	}
}

func TestDeniedSecureHaltAndUnsupportedControls(t *testing.T) {
	c := newCore(t, sim.M33, sim.Snapshot{})
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&(halt|halted) != 0 {
		t.Fatal("denied Secure halt executed")
	}
	c = newCore(t, sim.M0, sim.Snapshot{})
	for _, control := range []uint32{5, 9, 35} {
		before := c.Snapshot()
		if err := write(t, c, dhcsr, key|control); !errors.Is(err, sim.ErrUnsupported) {
			t.Fatal(err)
		}
		if c.Snapshot() != before {
			t.Fatal("unsupported write took effect")
		}
	}
	for _, addr := range []uint64{cpuid, dhcsr + 4} {
		if err := write(t, c, addr, 1); !errors.Is(err, dapsim.ErrBusFault) {
			t.Fatal(err)
		}
	}
}

func TestInterruptMaskRequiresStableHalt(t *testing.T) {
	c := newCore(t, sim.M0, sim.Snapshot{DHCSR: 3 | halted})
	if err := write(t, c, dhcsr, key|11); err != nil {
		t.Fatal(err)
	}
	before := c.Snapshot()
	if err := write(t, c, dhcsr, key|1); !errors.Is(err, sim.ErrUnsupported) || c.Snapshot() != before {
		t.Fatalf("mask changed with resume: %v", err)
	}
	if err := write(t, c, dhcsr, key|9); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted != 0 {
		t.Fatal("mask prevented resume")
	}
}

func TestEnableClearsUnknownDisabledInterruptMask(t *testing.T) {
	for _, p := range []sim.Profile{sim.M0, sim.M33} {
		initial := uint32(8)
		if p == sim.M33 {
			initial |= secure
		}
		c := newCore(t, p, sim.Snapshot{DHCSR: initial})
		if err := write(t, c, dhcsr, key|9); !errors.Is(err, sim.ErrUnsupported) {
			t.Fatalf("enabled with mask set: %v", err)
		}
		if c.Snapshot().DHCSR != initial {
			t.Fatal("unpredictable enable changed state")
		}
		if err := write(t, c, dhcsr, key|enable); err != nil {
			t.Fatalf("legal clearing write rejected: %v", err)
		}
		if c.Snapshot().DHCSR&9 != enable {
			t.Fatal("disabled UNKNOWN mask retained")
		}
	}
}

func TestSnapStallBitCanClearWithoutRecoveringMemory(t *testing.T) {
	c := newCore(t, sim.M33, sim.Snapshot{DHCSR: secure | 3 | halted | 32})
	if err := write(t, c, dhcsr, key|3); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&32 != 0 {
		t.Fatal("snap-stall bit did not clear")
	}
	before := c.Snapshot()
	if err := write(t, c, dhcsr, key|enable); err == nil || c.Snapshot() != before {
		t.Fatal("clearing snap-stall made memory safe to resume")
	}
}
