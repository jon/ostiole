package sim_test

import (
	"errors"
	"fmt"
	"testing"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/target/cortexm/sim"
)

func TestHaltedDebugCannotBeDisabled(t *testing.T) {
	for _, profile := range []sim.Profile{sim.M0, sim.M33} {
		t.Run(fmt.Sprint(profile), func(t *testing.T) {
			initial := sim.Snapshot{DHCSR: enable | halt | halted, DFSR: 17}
			if profile == sim.M33 {
				initial.DHCSR |= secure
			}
			c := newCore(t, profile, initial)
			if err := write(t, c, dhcsr, key); !errors.Is(err, sim.ErrUnsupported) || c.Snapshot() != initial {
				t.Fatalf("disable halted: %v, snapshot %+v want %+v", err, c.Snapshot(), initial)
			}
		})
	}
}

func TestExternalHaltRequiresSecurePermission(t *testing.T) {
	initial := sim.Snapshot{DHCSR: enable, DFSR: 3}
	c := newCore(t, sim.M33, initial)
	if err := c.Schedule(0, sim.ExternalHalt); err != nil {
		t.Fatal(err)
	}
	if err := c.Clock().Advance(0); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot(); got != initial {
		t.Fatalf("denied external halt changed state: %+v want %+v", got, initial)
	}
}

func TestInterruptMaskRequiresStableDebugState(t *testing.T) {
	for _, profile := range []sim.Profile{sim.M0, sim.M33} {
		for _, resuming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/resuming=%v", profile, resuming), func(t *testing.T) {
				checkInterruptMaskState(t, profile, resuming)
			})
		}
	}
}

func checkInterruptMaskState(t *testing.T, profile sim.Profile, resuming bool) {
	t.Helper()
	initial := sim.Snapshot{DHCSR: enable, DFSR: 17}
	if profile == sim.M33 {
		initial.DHCSR |= secure
	}
	if resuming {
		initial.DHCSR |= halt | halted
	}
	c, err := sim.New(sim.Config{Profile: profile, Initial: initial, ResumeDelay: 5})
	if err != nil {
		t.Fatal(err)
	}
	if resuming {
		if err := write(t, c, dhcsr, key|enable); err != nil {
			t.Fatal(err)
		}
	}
	before := c.Snapshot()
	if err := write(t, c, dhcsr, key|enable|halt|8); !errors.Is(err, sim.ErrUnsupported) || c.Snapshot() != before {
		t.Fatalf("mask change: %v, snapshot %+v want %+v", err, c.Snapshot(), before)
	}
	if err := c.Clock().Advance(4); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot() != before {
		t.Fatal("rejected mask change altered pending state")
	}
	if err := c.Clock().Advance(1); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().DHCSR&halted != 0 {
		t.Fatal("rejected mask change interrupted resume")
	}
}

func TestM33RestartReadClearsOnlyStickyStatus(t *testing.T) {
	const sticky = uint32(1<<24 | 1<<25 | 1<<26)
	c := newCore(t, sim.M33, sim.Snapshot{DHCSR: enable | halt | halted | secure | 1<<24 | 1<<25, DFSR: 19})
	if err := write(t, c, dhcsr, key|enable); err != nil {
		t.Fatal(err)
	}
	before := c.Snapshot()
	if before.DHCSR&sticky != sticky || before.DHCSR&halted != 0 {
		t.Fatalf("resume snapshot %+v", before)
	}
	if c.Snapshot() != before {
		t.Fatal("snapshot consumed sticky status")
	}
	if first := read(t, c, dhcsr); first != before.DHCSR {
		t.Fatalf("first DHCSR %#x want %#x", first, before.DHCSR)
	}
	if second := read(t, c, dhcsr); second != before.DHCSR & ^sticky {
		t.Fatalf("second DHCSR %#x want %#x", second, before.DHCSR & ^sticky)
	}
	if c.Snapshot().DFSR != before.DFSR {
		t.Fatal("DHCSR read changed DFSR")
	}
}

func TestDFSRWriteClearsSelectedReasons(t *testing.T) {
	c := newCore(t, sim.M33, sim.Snapshot{DHCSR: enable | secure | 1<<25, DFSR: 31})
	control := c.Snapshot().DHCSR
	for _, row := range []struct{ value, want uint32 }{{1, 30}, {8, 22}, {0, 22}, {1 << 31, 22}} {
		if err := write(t, c, dfsr, row.value); err != nil {
			t.Fatal(err)
		}
		if first, second := read(t, c, dfsr), read(t, c, dfsr); first != row.want || second != row.want {
			t.Fatalf("DFSR after %#x: %#x, %#x want %#x", row.value, first, second, row.want)
		}
		if c.Snapshot().DHCSR != control {
			t.Fatal("DFSR access changed DHCSR")
		}
	}
}

func TestInvalidWordAddressesDoNotAliasOrConsumeState(t *testing.T) {
	for _, addr := range []uint64{0x1e000edf0, 0x1e000ed00, 0x1e000ed30, dhcsr + 4, dhcsr + 1} {
		t.Run(fmt.Sprintf("%x", addr), func(t *testing.T) {
			initial := sim.Snapshot{DHCSR: enable | secure | 1<<24 | 1<<25 | restart, DFSR: 31}
			c := newCore(t, sim.M33, initial)
			if err := c.Read(t.Context(), addr, make([]byte, 4)); !errors.Is(err, dapsim.ErrBusFault) || c.Snapshot() != initial {
				t.Fatalf("invalid word read: %v, snapshot %+v", err, c.Snapshot())
			}
			if err := write(t, c, addr, key|enable|halt); !errors.Is(err, dapsim.ErrBusFault) || c.Snapshot() != initial {
				t.Fatalf("invalid word write: %v, snapshot %+v", err, c.Snapshot())
			}
		})
	}
}

func TestWarmResetClearsMaskAndRefreshesLatchedPermission(t *testing.T) {
	c := newCore(t, sim.M33, sim.Snapshot{DHCSR: enable | halt | halted | secure, DFSR: 31})
	if err := write(t, c, dhcsr, key|enable|halt|8); err != nil {
		t.Fatal(err)
	}
	if err := c.Schedule(0, sim.RevokeSecureDebug); err != nil {
		t.Fatal(err)
	}
	if err := c.Clock().Advance(0); err != nil {
		t.Fatal(err)
	}
	before := c.Snapshot()
	if before.DHCSR&(8|halt|halted|secure) != 8|halt|halted|secure {
		t.Fatalf("permission was not latched while halted: %+v", before)
	}
	if err := c.Schedule(0, sim.WarmReset); err != nil {
		t.Fatal(err)
	}
	if err := c.Clock().Advance(0); err != nil {
		t.Fatal(err)
	}
	want := sim.Snapshot{DHCSR: enable | 1<<25, DFSR: 31}
	if got := c.Snapshot(); got != want {
		t.Fatalf("warm reset snapshot %+v want %+v", got, want)
	}
}
