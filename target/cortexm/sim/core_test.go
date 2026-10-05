package sim_test

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/target/cortexm/sim"
)

const (
	cpuid   = uint64(0xe000ed00)
	dhcsr   = uint64(0xe000edf0)
	dfsr    = uint64(0xe000ed30)
	key     = uint32(0xa05f0000)
	enable  = uint32(1)
	halt    = uint32(2)
	halted  = uint32(1 << 17)
	secure  = uint32(1 << 20)
	restart = uint32(1 << 26)
)

func read(t *testing.T, c *sim.Core, addr uint64) uint32 {
	t.Helper()
	data := make([]byte, 4)
	if err := c.Read(t.Context(), addr, data); err != nil {
		t.Fatal(err)
	}
	return binary.LittleEndian.Uint32(data)
}
func write(t *testing.T, c *sim.Core, addr uint64, value uint32) error {
	t.Helper()
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, value)
	return c.Write(t.Context(), addr, data)
}
func newCore(t *testing.T, profile sim.Profile, initial sim.Snapshot) *sim.Core {
	t.Helper()
	c, err := sim.New(sim.Config{Profile: profile, Initial: initial})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIdentityAndDetachedObservations(t *testing.T) {
	for _, p := range []sim.Profile{sim.M0, sim.M33} {
		c := newCore(t, p, sim.Snapshot{DHCSR: 1<<24 | 1<<25})
		want := uint32(0x410cc200)
		if p == sim.M33 {
			want = 0x411fd210
		}
		if got := read(t, c, cpuid); got != want {
			t.Fatalf("CPUID %#x want %#x", got, want)
		}
		before := c.Snapshot()
		if before.DHCSR&(1<<24|1<<25) == 0 {
			t.Fatal("snapshot consumed status")
		}
		first := read(t, c, dhcsr)
		second := read(t, c, dhcsr)
		if first&(1<<24|1<<25) != 1<<24|1<<25 || second&(1<<24|1<<25) != 0 {
			t.Fatalf("sticky reads %#x %#x", first, second)
		}
		if c.Snapshot().DHCSR&halted != 0 {
			t.Fatal("read completed a halt")
		}
	}
}

func TestInvalidProfilesAndAccessesHaveNoEffects(t *testing.T) {
	for _, cfg := range []sim.Config{
		{},
		{Profile: sim.Profile(99)},
		{Profile: sim.M0, Initial: sim.Snapshot{DHCSR: secure}},
		{Profile: sim.M0, Initial: sim.Snapshot{DFSR: 32}},
		{Profile: sim.M0, Initial: sim.Snapshot{DHCSR: halted}},
		{Profile: sim.M33, Initial: sim.Snapshot{DHCSR: 3 | halted}},
	} {
		if c, err := sim.New(cfg); c != nil || err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	c := newCore(t, sim.M0, sim.Snapshot{DHCSR: 1 << 25})
	for _, addr := range []uint64{dhcsr, dhcsr + 1, dhcsr + 4, 0x1e000edf0} {
		for _, size := range []int{0, 1, 2, 3, 8} {
			if err := c.Read(t.Context(), addr, make([]byte, size)); !errors.Is(err, dapsim.ErrBusFault) {
				t.Fatalf("read %#x/%d: %v", addr, size, err)
			}
		}
	}
	var zero sim.Core
	if err := zero.Read(t.Context(), dhcsr, make([]byte, 4)); err == nil {
		t.Fatal("zero core usable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Read(ctx, dhcsr, make([]byte, 4)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.Write(ctx, dhcsr, make([]byte, 4)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var nilContext context.Context
	if err := c.Read(nilContext, dhcsr, make([]byte, 4)); err == nil {
		t.Fatal("nil context accepted")
	}
	if c.Snapshot().DHCSR&(1<<25) == 0 {
		t.Fatal("invalid read consumed status")
	}
}

func TestInitialDFSRAndUnsupportedAddressWrite(t *testing.T) {
	c := newCore(t, sim.M0, sim.Snapshot{DFSR: 31})
	if got := read(t, c, dfsr); got != 31 {
		t.Fatalf("DFSR %#x", got)
	}
	before := c.Snapshot()
	if err := write(t, c, cpuid, 0); !errors.Is(err, dapsim.ErrBusFault) || c.Snapshot() != before {
		t.Fatal("write changed read-only identity")
	}
}
