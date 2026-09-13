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

// shareAPModel uses the existing register/memory model, without its SWD wire.
// The JTAG peer above owns acceptance, delayed completion, and acknowledgements.
func shareAPModel(t testing.TB, model *jtagDPModel, target *sim.Target) {
	t.Helper()
	model.complete = func(r jtagRequest) uint32 {
		ctx := context.Background()
		if err := target.Write(ctx, swdsim.Request{Addr: 8}, model.selectDP); err != nil {
			t.Fatal(err)
		}
		req := swdsim.Request{AP: true, Read: r.read, Addr: r.addr}
		if !r.read {
			if err := target.Write(ctx, req, r.data); err != nil {
				t.Fatal(err)
			}
			return 0
		}
		if _, err := target.Read(ctx, req); err != nil {
			t.Fatal(err)
		}
		value, err := target.Read(ctx, swdsim.Request{Read: true, Addr: 12})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
}

func TestSharedMEMAPScalarBlockAndRestoration(t *testing.T) {
	for _, link := range []string{"SWD", "JTAG"} {
		for _, bigEndian := range []bool{false, true} {
			t.Run(link+map[bool]string{false: "/LE", true: "/BE"}[bigEndian], func(t *testing.T) {
				exerciseSharedMEMAP(t, link, bigEndian)
			})
		}
	}
}

func exerciseSharedMEMAP(t *testing.T, link string, bigEndian bool) {
	t.Helper()
	target := sim.New(0x2ba01477)
	sel := dap.NewAPSel(1)
	if err := target.AddMEMAP(sel, 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	cfg := uint32(6)
	if bigEndian {
		cfg |= 1
	}
	if err := target.SetMEMAPCFG(sel, cfg); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPDebugBase(sel, 0xe00ff003, 1); err != nil {
		t.Fatal(err)
	}
	port := dap.SWDP(swd.New(swdsim.New(target)))
	if link == "JTAG" {
		model, wire, chain := jtagModelChain(t, 8, 1)
		wire.limit = 13
		shareAPModel(t, model, target)
		port = dap.JTAGDP(chain, 1)
	}
	dp := dap.NewDebugPort(port)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	for addr, value := range map[uint8]uint32{0: 0xa5000051, 4: 0x9988, 8: 1} {
		if err := dp.WriteRawAP(t.Context(), sel.Address(addr), value); err != nil {
			t.Fatal(err)
		}
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	base, present, err := mem.ReadDebugBase(t.Context())
	if err != nil || !present || base != 0x1e00ff000 {
		t.Fatalf("debug base: %#x, %v, %v", base, present, err)
	}
	exerciseSharedMemory(t, mem)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[uint8]uint32{0: 0xa5000051, 4: 0x9988, 8: 1} {
		if got, err := dp.ReadRawAP(t.Context(), sel.Address(addr)); err != nil || got != want {
			t.Fatalf("restored AP %#x = %#x, %v", addr, got, err)
		}
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func exerciseSharedMemory(t *testing.T, mem *dap.MemAP) {
	t.Helper()
	for _, size := range []dap.TransferSize{dap.Size8, dap.Size16, dap.Size32, dap.Size64} {
		if err := mem.WriteScalar(t.Context(), 0x1_00000200, size, 0x42); err != nil {
			t.Fatal(err)
		}
		if value, err := mem.ReadScalar(t.Context(), 0x1_00000200, size); err != nil || value != 0x42 {
			t.Fatalf("scalar: %#x, %v", value, err)
		}
	}
	data := make([]byte, 1031)
	for i := range data {
		data[i] = byte(i * 17)
	}
	if n, err := mem.WriteBlock(t.Context(), 0x1_000003fd, data); err != nil || n != len(data) {
		t.Fatalf("block write %d: %v", n, err)
	}
	got := make([]byte, len(data))
	if n, err := mem.ReadBlock(t.Context(), 0x1_000003fd, got); err != nil || n != len(data) {
		t.Fatalf("block read %d: %v", n, err)
	}
	if !slices.Equal(got, data) {
		t.Fatal("block data differs")
	}
}

func TestJTAGBlockFaultPreservesConfirmedPrefix(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[write], func(t *testing.T) {
			exerciseJTAGBlockFault(t, write)
		})
	}
}

func exerciseJTAGBlockFault(t *testing.T, write bool) {
	t.Helper()
	m, wire, chain := jtagModelChain(t, 4, 0)
	target := sim.New(0x2ba01477)
	sel := dap.NewAPSel(1)
	if err := target.AddMEMAP(sel, 0x24770011, map[uint32]uint32{0x100: 1, 0x104: 2, 0x108: 3, 0x10c: 4}); err != nil {
		t.Fatal(err)
	}
	shareAPModel(t, m, target)
	dp := dap.NewDebugPort(dap.JTAGDP(chain, 0))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	complete := m.complete
	effects := 0
	m.complete = func(r jtagRequest) uint32 {
		if m.selectDP&0xf0 == 0 && r.addr == 12 && r.read != write {
			effects++
			if effects == 3 {
				m.ctrl |= 1 << 5
				return 0
			}
		}
		return complete(r)
	}
	buf := make([]byte, 16)
	for i := range buf {
		buf[i] = 0xaa
	}
	var n int
	if write {
		n, err = mem.WriteBlock(t.Context(), 0x100, buf)
	} else {
		n, err = mem.ReadBlock(t.Context(), 0x100, buf)
	}
	if n != 8 || !errors.Is(err, dap.ErrFault) || effects != 3 {
		t.Fatalf("prefix=%d effects=%d error=%v", n, effects, err)
	}
	if write {
		assertJTAGWrittenPrefix(t, target, sel, err)
		assertJTAGMEMAPInvalid(t, mem, wire)
	} else if !slices.Equal(buf, []byte{1, 0, 0, 0, 2, 0, 0, 0, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa}) {
		t.Fatalf("read suffix modified: %x", buf)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func assertJTAGMEMAPInvalid(t *testing.T, mem *dap.MemAP, wire *jtagDPWire) {
	t.Helper()
	before := wire.calls
	_, readErr := mem.ReadWord(t.Context(), 0x100)
	writeErr := mem.WriteScalar(t.Context(), 0x100, dap.Size32, 42)
	if readErr == nil || writeErr == nil || wire.calls != before {
		t.Fatal("uncertain write allowed another memory access")
	}
}

func assertJTAGWrittenPrefix(t *testing.T, target *sim.Target, sel dap.APSel, err error) {
	t.Helper()
	if !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("uncertain write: %v", err)
	}
	got, err := target.MEMAPBytes(sel, 0x100, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []byte{0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 3, 0, 0, 0, 4, 0, 0, 0}) {
		t.Fatalf("write prefix/suffix: %x", got)
	}
}
