package armdebug_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/probe"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func connectMemoryBench(t *testing.T) (*armdebug.Conn, *bench) {
	t.Helper()
	b := newBench()
	for _, ap := range []uint8{0, 1} {
		if err := b.target.AddMEMAP(dap.NewAPSel(ap), 0x24770011, map[uint32]uint32{0x20000000: uint32(ap) + 10}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	return c, b
}

func TestOwnedMemAPsRestoreDistinctAPs(t *testing.T) {
	c, b := connectMemoryBench(t)
	memories := make([]*dap.MemAP, 2)
	for i := range memories {
		ap := dap.NewAPSel(uint8(i))
		if err := c.Port().WriteRawAP(t.Context(), ap.Address(0), 0x23000052); err != nil {
			t.Fatal(err)
		}
		if err := c.Port().WriteRawAP(t.Context(), ap.Address(4), uint32(0x100+i*4)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range memories {
		m, err := c.OpenMemAP(t.Context(), dap.NewAPSel(uint8(i)))
		if err != nil {
			t.Fatal(err)
		}
		memories[i] = m
	}
	for _, i := range []int{0, 1, 0, 1} {
		value, err := memories[i].ReadWord(t.Context(), 0x20000000)
		if err != nil || value != uint32(i)+10 {
			t.Fatalf("AP%d interleaved read: %d, %v", i, value, err)
		}
	}
	b.beforeClose = func() {
		for i := range memories {
			// Inspect the simulator only after the owning protocol has released it.
			if err := b.target.Write(t.Context(), swdsim.Request{Addr: 8}, uint32(i)<<24); err != nil {
				t.Fatal(err)
			}
			for reg, want := range map[uint8]uint32{0: 0x23000052, 4: uint32(0x100 + i*4)} {
				if _, err := b.target.Read(t.Context(), swdsim.Request{AP: true, Read: true, Addr: reg}); err != nil {
					t.Fatal(err)
				}
				value, err := b.target.Read(t.Context(), swdsim.Request{Read: true, Addr: 12})
				if err != nil || value != want {
					t.Fatalf("AP%d register%d not restored: %#x %v", i, reg, value, err)
				}
			}
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMemAPPreflightAndAcquisitionFailure(t *testing.T) {
	c, b := connectMemoryBench(t)
	if _, err := c.OpenMemAP(t.Context(), dap.NewAPSel(0)); err != nil {
		t.Fatal(err)
	}
	before := b.transfers
	for _, ap := range []dap.APSel{{}, dap.NewAPSel(0)} {
		if _, err := c.OpenMemAP(t.Context(), ap); err == nil || b.transfers != before {
			t.Fatal("invalid or duplicate AP reached wire")
		}
	}
	var nilContext context.Context
	if _, err := c.OpenMemAP(nilContext, dap.NewAPSel(1)); err == nil || b.transfers != before {
		t.Fatal("nil context reached wire")
	}
	if _, err := c.OpenMemAP(canceledContext(t), dap.NewAPSel(1)); !errors.Is(err, context.Canceled) || b.transfers != before {
		t.Fatal("canceled context reached wire")
	}
	if _, err := c.OpenMemAP(t.Context(), dap.NewAPSel(2)); err == nil {
		t.Fatal("absent AP opened")
	}
	if _, err := c.OpenMemAP(t.Context(), dap.NewAPSel(1)); err != nil {
		t.Fatalf("failed acquisition discarded owner: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenMemAP(t.Context(), dap.NewAPSel(1)); err == nil {
		t.Fatal("closed owner acquired AP")
	}
}

func TestMemAPCleanupFailureKeepsProbe(t *testing.T) {
	c, b := connectMemoryBench(t)
	m, err := c.OpenMemAP(t.Context(), dap.NewAPSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadWord(t.Context(), 0x20000000); err != nil {
		t.Fatal(err)
	}
	want := errors.New("restore failed")
	b.wireErr = want
	if err := c.Close(); !errors.Is(err, want) || b.closes != 0 || c.Port() != nil {
		t.Fatalf("cleanup discarded dependencies: %v", err)
	}
	b.wireErr = nil
	if err := c.Close(); err != nil || b.closes != 1 {
		t.Fatalf("cleanup retry: %v", err)
	}
}

func TestOwnedAPv2Memory(t *testing.T) {
	b := newBench()
	b.target.Target = dapsim.New(0x4c013477)
	if err := b.target.SetDPRegister(dap.DPIDR1, 20); err != nil {
		t.Fatal(err)
	}
	sel, err := dap.APAt(0x2000)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.target.AddMEMAP(sel, 0x34770008, map[uint32]uint32{0x100: 7}); err != nil {
		t.Fatal(err)
	}
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	mem, err := c.OpenMemAP(t.Context(), sel)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mem.ReadWord(t.Context(), 0x100); err != nil || got != 7 {
		t.Fatalf("read=%#x,%v", got, err)
	}
	before := b.transfers
	if _, err := c.OpenMemAP(t.Context(), sel); err == nil || b.transfers != before {
		t.Fatal("duplicate AP reached hardware")
	}
	failure := errors.New("restore failed")
	b.target.beforeWrite = func(req swdsim.Request, selected, value uint32) error {
		if req.AP && req.Addr == 4 && selected&^15 == 0x2d00 {
			return failure
		}
		return nil
	}
	if err := c.Close(); !errors.Is(err, failure) || b.closes != 0 {
		t.Fatalf("close=%v, probe closes=%d", err, b.closes)
	}
	b.target.beforeWrite = nil
	if err := c.Close(); err != nil || b.closes != 1 {
		t.Fatalf("retry=%v, probe closes=%d", err, b.closes)
	}
}
