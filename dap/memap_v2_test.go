package dap_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
)

func TestAPv2MemoryAndRestoration(t *testing.T) {
	dp, sel := newAPv2MemoryPort(t)
	defer func() {
		if err := dp.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	for offset, value := range map[uint16]uint32{0xd00: 0x23000042, 0xd04: 0x20000100} {
		if err := dp.WriteRawAP(t.Context(), sel.Address(offset), value); err != nil {
			t.Fatal(err)
		}
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil || got != 0x411fd210 {
		t.Fatalf("CPUID=%#x,%v", got, err)
	}
	if base, present, err := mem.ReadDebugBase(t.Context()); err != nil || !present || base != 0xe00ff000 {
		t.Fatalf("base=%#x,%v,%v", base, present, err)
	}
	checkAPv2BlockRoundTrip(t, mem)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	for offset, want := range map[uint16]uint32{0xd00: 0x23000042, 0xd04: 0x20000100} {
		got, err := dp.ReadRawAP(t.Context(), sel.Address(offset))
		if err != nil || got != want {
			t.Fatalf("restore %#x=%#x,%v", offset, got, err)
		}
	}
}

func TestAPv2ReleaseRetriesAfterFramingLoss(t *testing.T) {
	target := newWaitTarget()
	target.Target = dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, 40); err != nil {
		t.Fatal(err)
	}
	sel, _ := dap.APAt(0x100002000)
	if err := target.AddMEMAP(sel, 0x34770008, map[uint32]uint32{0x100: 7}); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), sel.Address(0xd04), 0x200); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("lost USB response")
	target.writeErrFor = apWrite(4)
	target.writeErr = failure
	if _, err := mem.ReadWord(t.Context(), 0x100); !errors.Is(err, failure) {
		t.Fatalf("read=%v", err)
	}
	target.writeErr = failure
	if err := mem.Release(t.Context()); err == nil {
		t.Fatal("cleanup succeeded despite continuing transfer failure")
	}
	target.writeErr = nil
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := dp.ReadRawAP(t.Context(), sel.Address(0xd04))
	if err != nil || got != 0x200 {
		t.Fatalf("restored TAR=%#x,%v", got, err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAPv2RejectsDeferredErrorModes(t *testing.T) {
	for _, csw := range []uint32{1 << 16, 1 << 17} {
		t.Run(fmt.Sprintf("%x", csw), func(t *testing.T) {
			target := newWaitTarget()
			target.Target = dapsim.New(0x4c013477)
			if err := target.SetDPRegister(dap.DPIDR1, 20); err != nil {
				t.Fatal(err)
			}
			sel, _ := dap.APAt(0x2000)
			if err := target.AddMEMAP(sel, 0x34770008, nil); err != nil {
				t.Fatal(err)
			}
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := dp.WriteRawAP(t.Context(), sel.Address(0xd00), csw); err != nil {
				t.Fatal(err)
			}
			if _, err := dap.OpenMemAP(t.Context(), dp, sel); err == nil {
				t.Fatal("accepted a MEM-AP that can hide errors")
			}
			before := len(target.requests)
			if err := dp.WriteDP(t.Context(), dap.CTRLSTAT, 1<<24|1); err == nil {
				t.Fatal("accepted DP ERRMODE")
			}
			if len(target.requests) != before {
				t.Fatal("invalid DP mode reached hardware")
			}
			if err := dp.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func newAPv2MemoryPort(t *testing.T) (*dap.DebugPort, dap.APSel) {
	t.Helper()
	target := newWaitTarget()
	target.Target = dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, 40); err != nil {
		t.Fatal(err)
	}
	sel, err := dap.APAt(0x100002000)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.AddMEMAP(sel, 0x34770008, map[uint32]uint32{0xe000ed00: 0x411fd210}); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPDebugBase(sel, 0xe00ff003, 0); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	return dp, sel
}

func checkAPv2BlockRoundTrip(t *testing.T, mem *dap.MemAP) {
	t.Helper()
	data := bytes.Repeat([]byte{1, 2, 3, 4}, 32)
	if n, err := mem.WriteBlock(t.Context(), 0x200003e1, data); err != nil || n != len(data) {
		t.Fatalf("write=%d,%v", n, err)
	}
	got := make([]byte, len(data))
	if n, err := mem.ReadBlock(t.Context(), 0x200003e1, got); err != nil || n != len(data) || !bytes.Equal(got, data) {
		t.Fatalf("read=%d,%v %x", n, err, got)
	}
}
