package dap_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestReadMEMAPDebugBaseFormats(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		cfg, low, high          uint32
		want                    uint64
		present, invalid, upper bool
	}{
		{name: "modern", low: 0xe00ff003, want: 0xe00ff000, present: true},
		{name: "modern zero", low: 3, present: true},
		{name: "modern absent", low: 0xe00ff002},
		{name: "legacy", low: 0x80000000, want: 0x80000000, present: true},
		{name: "legacy zero", present: true},
		{name: "legacy absent", low: 0xffffffff},
		{name: "large", cfg: 2, low: 0x12345003, high: 0xfedcba98, want: 0xfedcba9812345000, present: true, upper: true},
		{name: "last page", cfg: 3, low: 0xfffff003, high: 0xffffffff, want: 0xfffffffffffff000, present: true, upper: true},
		{name: "large absent", cfg: 2, low: 2, high: 0x1234},
		{name: "large legacy", cfg: 2, low: 0x80000000, invalid: true},
		{name: "large legacy absent", cfg: 2, low: 0xffffffff, invalid: true},
		{name: "modern reserved", low: 7, invalid: true},
		{name: "legacy bit zero", low: 0x80000001, invalid: true},
		{name: "legacy reserved", low: 0x80000004, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mem, _, target := debugBaseMemory(t, tt.cfg, tt.low, tt.high)
			before := len(target.requests)
			got, present, err := mem.ReadDebugBase(t.Context())
			if (err != nil) != tt.invalid || got != tt.want || present != tt.present {
				t.Fatalf("ReadDebugBase()=%#x,%v,%v", got, present, err)
			}
			wantReads := []swdsim.Request{apRead(8)}
			if tt.upper {
				wantReads = append(wantReads, apRead(0))
			}
			var reads []swdsim.Request
			for _, req := range target.requests[before:] {
				if req.AP {
					reads = append(reads, req)
				}
			}
			if !slices.Equal(reads, wantReads) {
				t.Fatalf("AP requests=%v, want %v", reads, wantReads)
			}
			if value, err := mem.ReadScalar(t.Context(), 0x100, dap.Size8); err != nil || value != 9 {
				t.Fatalf("memory no longer usable: %#x,%v", value, err)
			}
		})
	}
}

func debugBaseMemory(t *testing.T, cfg, low, high uint32) (*dap.MemAP, *dap.DebugPort, *waitTarget) {
	t.Helper()
	target := newWaitTarget()
	addMEMAP(t, target, 17, 0x04770021, map[uint32]uint32{0x100: 9})
	if err := target.SetMEMAPCFG(apSel(17), cfg); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPDebugBase(apSel(17), low, high); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseDebugBaseTest(t, dp.Release) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(17))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseDebugBaseTest(t, mem.Release) })
	return mem, dp, target
}

func releaseDebugBaseTest(t *testing.T, release func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := release(ctx); err != nil {
		t.Error(err)
	}
}

func TestReadMEMAPDebugBaseRejectsInactiveClientBeforeTraffic(t *testing.T) {
	for _, state := range []string{"nil context", "canceled", "invalidated", "disconnected"} {
		t.Run(state, func(t *testing.T) {
			mem, dp, target := debugBaseMemory(t, 0, 3, 0)
			ctx := t.Context()
			switch state {
			case "nil context":
				ctx = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "invalidated":
				if _, err := dp.ReadRawAP(ctx, apSel(17).Address(0xfc)); err != nil {
					t.Fatal(err)
				}
			case "disconnected":
				if err := dp.Release(ctx); err != nil {
					t.Fatal(err)
				}
			}
			before := len(target.requests)
			base, present, err := mem.ReadDebugBase(ctx)
			if err == nil || base != 0 || present || len(target.requests) != before {
				t.Fatalf("result=%#x,%v,%v; requests=%d", base, present, err, len(target.requests)-before)
			}
		})
	}
	for _, mem := range []*dap.MemAP{nil, {}} {
		if _, _, err := mem.ReadDebugBase(t.Context()); err == nil {
			t.Fatal("nil/zero client accepted")
		}
	}
}

func TestReadMEMAPDebugBaseFailureAndRetry(t *testing.T) {
	for _, stage := range []struct {
		name    string
		request swdsim.Request
	}{
		{"BASE", apRead(8)}, {"upper", apRead(0)}, {"completion", dpRead(12)},
	} {
		t.Run(stage.name, func(t *testing.T) {
			mem, dp, target := debugBaseMemory(t, 2, 0x1003, 2)
			if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
				t.Fatal(err)
			}
			target.armFault(stage.request)
			if stage.name == "completion" {
				target.waitSkip = 1
			}
			base, present, err := mem.ReadDebugBase(t.Context())
			if !errors.Is(err, dap.ErrFault) || base != 0 || present || !strings.Contains(err.Error(), "BASE") {
				t.Fatalf("result=%#x,%v,%v", base, present, err)
			}
			releaseDebugBaseTest(t, mem.Release)
			for _, reg := range []uint8{0, 4, 8} {
				got, readErr := dp.ReadRawAP(t.Context(), apSel(17).Address(uint16(reg)))
				if readErr != nil || got != 0 {
					t.Fatalf("restored register %#x=%#x, %v", reg, got, readErr)
				}
			}
			mem, err = dap.OpenMemAP(t.Context(), dp, apSel(17))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { releaseDebugBaseTest(t, mem.Release) })
			if base, present, err = mem.ReadDebugBase(t.Context()); err != nil || !present || base != 0x200001000 {
				t.Fatalf("retry=%#x,%v,%v", base, present, err)
			}
		})
	}
}

func TestReadMEMAPDebugBaseRetriesWAIT(t *testing.T) {
	mem, _, target := debugBaseMemory(t, 0, 0x1003, 0)
	target.arm(apRead(8), 2)
	base, present, err := mem.ReadDebugBase(t.Context())
	if err != nil || !present || base != 0x1000 || target.attempts != 3 {
		t.Fatalf("result=%#x,%v,%v; attempts=%d", base, present, err, target.attempts)
	}
}

func TestReadMEMAPDebugBaseCancellationStopsBeforeUpperWord(t *testing.T) {
	target := &cancelAPTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target, 0, 0x04770021, nil)
	if err := target.SetMEMAPCFG(apSel(0), 2); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPDebugBase(apSel(0), 0x1003, 2); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseDebugBaseTest(t, dp.Release) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseDebugBaseTest(t, mem.Release) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	target.cancel = cancel
	target.req = apRead(8)
	target.afterBarrier = true
	before := len(target.requests)
	base, present, err := mem.ReadDebugBase(ctx)
	if !errors.Is(err, context.Canceled) || base != 0 || present {
		t.Fatalf("result=%#x,%v,%v", base, present, err)
	}
	for _, req := range target.requests[before:] {
		if req == apRead(0) {
			t.Fatal("read upper word after cancellation")
		}
	}
}
