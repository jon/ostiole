package dap_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type cancelAfterSelectWire struct {
	swd.Wire
	target *waitTarget
	cancel context.CancelFunc
	calls  int
}

func (w *cancelAfterSelectWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	input, err := w.Wire.SWDIO(ctx, direction, output, bits)
	values := w.target.selectValues
	if err == nil && w.cancel != nil && len(values) != 0 && values[len(values)-1]&15 == 5 {
		w.cancel()
		w.cancel = nil
	}
	return input, err
}

func newWideSelectPort(t *testing.T, width uint32, options ...dap.Option) (*dap.DebugPort, *waitTarget, *cancelAfterSelectWire) {
	t.Helper()
	target := newWaitTarget()
	target.Target = dapsim.New(0x4c013477)
	if err := target.SetDPRegister(dap.DPIDR1, width); err != nil {
		t.Fatal(err)
	}
	low, _ := dap.APAt(0)
	high, _ := dap.APAt(uint64(1) << (width - 1))
	for sel, id := range map[dap.APSel]uint32{low: 0x34770008, high: 0x24770011} {
		if err := target.AddAP(sel, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := target.SetDebugWord(0, 0x13579bdf); err != nil {
		t.Fatal(err)
	}
	wire := &cancelAfterSelectWire{Wire: swdsim.New(target), target: target}
	dp := dap.NewDebugPort(dap.SWDP(swd.New(wire)), options...)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dp.Release(ctx); err != nil {
			t.Error(err)
		}
	})
	return dp, target, wire
}

func TestPendingSELECTBeforeBankedAccess(t *testing.T) {
	for _, width := range []uint32{40, 48, 52} {
		for _, access := range []struct {
			reg   dap.DPRegister
			write bool
			value uint32
		}{
			{reg: dap.CTRLSTAT, value: 0xf0000001},
			{reg: dap.CTRLSTAT, write: true, value: 0x50000001},
			{reg: dap.DLCR}, {reg: dap.DPIDR1, value: width},
			{reg: dap.SELECT1, write: true, value: 1},
		} {
			t.Run(fmt.Sprintf("%d/%s/write=%t", width, access.reg, access.write), func(t *testing.T) {
				dp, target, _ := newWideSelectPort(t, width)
				if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
					t.Fatal(err)
				}
				before := len(target.requests)
				if access.write {
					if err := dp.WriteDP(t.Context(), access.reg, access.value); err != nil {
						t.Fatal(err)
					}
				} else if value, err := dp.ReadDP(t.Context(), access.reg); err != nil || value != access.value {
					t.Fatalf("%s = %#x, %v; want %#x", access.reg, value, err, access.value)
				}
				if got := target.requests[before]; got != dpRead(12) {
					t.Fatalf("first request = %+v, want RDBUFF", got)
				}
			})
		}
	}
}

func TestPendingSELECTBeforeWideAPAccess(t *testing.T) {
	for _, width := range []uint32{40, 48, 52} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			dp, target, _ := newWideSelectPort(t, width)
			if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
				t.Fatal(err)
			}
			before := len(target.requests)
			high, _ := dap.APAt(uint64(1) << (width - 1))
			id, err := dp.ReadAPIDR(t.Context(), high)
			if err != nil || id.Raw != 0x24770011 {
				t.Fatalf("high AP IDR = %#x, %v", id.Raw, err)
			}
			if got := target.requests[before]; got != dpRead(12) {
				t.Fatalf("first request = %+v, want RDBUFF", got)
			}
		})
	}
}

func TestWideSELECTReleaseRetry(t *testing.T) {
	for _, width := range []uint32{40, 48, 52} {
		for _, failure := range []string{"none", "cancel", "WAIT"} {
			t.Run(fmt.Sprintf("%d/%s", width, failure), func(t *testing.T) {
				checkWideSELECTReleaseRetry(t, width, failure)
			})
		}
	}
}

func checkWideSELECTReleaseRetry(t *testing.T, width uint32, failure string) {
	t.Helper()
	dp, target, wire := newWideSelectPort(t, width, dap.WithMaxWaits(1))
	high, _ := dap.APAt(uint64(1) << (width - 1))
	if _, err := dp.ReadAPIDR(t.Context(), high); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteDP(t.Context(), dap.SELECT, 1); err != nil {
		t.Fatal(err)
	}
	if failure != "none" {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		want := dap.ErrWait
		if failure == "cancel" {
			wire.cancel = cancel
			want = context.Canceled
		} else {
			target.arm(dpRead(12), 1)
		}
		if err := dp.Release(ctx); !errors.Is(err, want) {
			t.Fatalf("first Release = %v, want %v", err, want)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := wire.calls
	if err := dp.Release(ctx); !errors.Is(err, context.Canceled) || wire.calls != calls {
		t.Fatalf("canceled Release = %v, wire calls = %d; want %d", err, wire.calls, calls)
	}
	before := len(target.requests)
	selectionCount := len(target.selectValues)
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if failure == "cancel" {
		for _, value := range target.selectValues[selectionCount:] {
			if value&15 == 5 {
				t.Fatal("replayed the acknowledged bank-five selection after cancellation")
			}
		}
	}
	if len(target.requests) == before {
		t.Fatal("release retry sent no traffic")
	}
	assertWideSelectionReleased(t, target)
	before = len(target.requests)
	if err := dp.Release(t.Context()); err != nil || len(target.requests) != before {
		t.Fatalf("completed Release = %v, requests = %d; want %d", err, len(target.requests), before)
	}
}

func assertWideSelectionReleased(t *testing.T, target *waitTarget) {
	t.Helper()
	id, err := target.Target.Read(t.Context(), dpRead(0))
	if err != nil || id != 0x4c013477 {
		t.Fatalf("released bank-zero DPIDR = %#x, %v", id, err)
	}
	state, err := target.Target.Read(t.Context(), dpRead(4))
	if err != nil || state&0xf0000001 != 0 {
		t.Fatalf("released CTRL/STAT = %#x, %v", state, err)
	}
	if _, err := target.Target.Read(t.Context(), apRead(0)); err != nil {
		t.Fatal(err)
	}
	word, err := target.Target.Read(t.Context(), dpRead(12))
	if err != nil || word != 0x13579bdf {
		t.Fatalf("debug word without replacing SELECT or SELECT1 = %#x, %v", word, err)
	}
}

func TestAPv2ASIZE32RejectsHighBaseBeforeTraffic(t *testing.T) {
	for _, operation := range []string{"IDR", "raw", "MEM-AP"} {
		t.Run(operation, func(t *testing.T) {
			dp, target, _ := newWideSelectPort(t, 32)
			outside, _ := dap.APAt(1 << 32)
			before := len(target.requests)
			var err error
			switch operation {
			case "IDR":
				_, err = dp.ReadAPIDR(t.Context(), outside)
			case "raw":
				_, err = dp.ReadRawAP(t.Context(), outside.Address(0xdfc))
			case "MEM-AP":
				_, err = dap.OpenMemAP(t.Context(), dp, outside)
			}
			if err == nil || len(target.requests) != before {
				t.Fatalf("out-of-range access = %v, requests = %d; want rejection with %d requests", err, len(target.requests), before)
			}
		})
	}
}
