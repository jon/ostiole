package coresight_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jon/ostiole/coresight"
	"github.com/jon/ostiole/dap"
)

func walkMemory() *componentMemory {
	m := memoryAt(0x10000, 1)
	for _, c := range []struct {
		base  uint64
		class uint8
	}{{0x20000, 9}, {0x30000, 0xe}, {0x40000, 0xe}} {
		for a, v := range memoryAt(c.base, c.class).words {
			m.words[a] = v
		}
	}
	m.words[0x20000+0xfbc] = 0x47700af7
	m.words[0x20000+0xfc8] = 1
	m.words[0x10000] = 0x10003
	m.words[0x10004] = 0x30003
	m.words[0x10008] = 0
	m.words[0x20000] = 0x10003
	m.words[0x20004] = 0
	m.words[0x20008] = 0
	m.words[0x2000c] = 0
	return m
}

func walkLimits() coresight.WalkLimits {
	return coresight.WalkLimits{MaxDepth: 4, MaxComponents: 16, MaxEntries: 32}
}

func TestWalkROMDepthFirst(t *testing.T) {
	m := walkMemory()
	visits, err := coresight.Walk(t.Context(), m, 0x10000, walkLimits())
	if err != nil {
		t.Fatal(err)
	}
	var bases []uint64
	var parents []int
	for _, v := range visits {
		if v.Component == nil || v.Err != nil {
			t.Fatalf("visit=%+v", v)
		}
		bases = append(bases, v.Component.Base)
		parents = append(parents, v.Parent)
	}
	if !slices.Equal(bases, []uint64{0x10000, 0x20000, 0x30000, 0x40000}) || !slices.Equal(parents, []int{-1, 0, 1, 0}) {
		t.Fatalf("bases=%x parents=%v", bases, parents)
	}
	if visits[0].Index != -1 || visits[3].Index != 1 || visits[2].Entry.Raw != 0x10003 {
		t.Fatalf("visits=%+v", visits)
	}
}

func TestWalkSkipsPowerDomains(t *testing.T) {
	m := walkMemory()
	m.words[0x10000] = 0x101f7
	visits, err := coresight.Walk(t.Context(), m, 0x10000, walkLimits())
	if !errors.Is(err, coresight.ErrPowerDomain) || len(visits) != 3 {
		t.Fatalf("visits=%+v err=%v", visits, err)
	}
	skipped := visits[1]
	if skipped.Component != nil || !errors.Is(skipped.Err, coresight.ErrPowerDomain) || skipped.Entry.PowerID != 31 || !skipped.Entry.PowerIDValid {
		t.Fatalf("skipped=%+v", skipped)
	}
	for _, a := range m.reads {
		if a >= 0x20000 && a < 0x21000 {
			t.Fatalf("accessed another power domain at %x", a)
		}
	}
	if visits[2].Component.Base != 0x40000 {
		t.Fatal("did not inspect accessible sibling")
	}
}

func TestWalkStopsAtRepeatedTables(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		m := walkMemory()
		if cycle {
			m.words[0x20000] = 0xffff0003
			m.words[0x20004] = 0xffffffff
		} else {
			m.words[0x10004] = 0x10003
		}
		visits, err := coresight.Walk(t.Context(), m, 0x10000, walkLimits())
		if !errors.Is(err, coresight.ErrRepeatedTable) {
			t.Fatalf("visits=%+v err=%v", visits, err)
		}
		last := visits[len(visits)-1]
		if last.Component != nil || !errors.Is(last.Err, coresight.ErrRepeatedTable) {
			t.Fatalf("last=%+v", last)
		}
		repeated := uint64(0x20000)
		if cycle {
			repeated = 0x10000
		}
		count := 0
		for _, a := range m.reads {
			if a == repeated+0xff0 {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("identified repeated table %d times", count)
		}
	}
}

func TestWalkLimits(t *testing.T) {
	for _, tt := range []struct {
		name   string
		limits coresight.WalkLimits
		count  int
		unread uint64
	}{
		{"depth", coresight.WalkLimits{MaxDepth: 0, MaxComponents: 16, MaxEntries: 32}, 1, 0x20ff0},
		{"components", coresight.WalkLimits{MaxDepth: 4, MaxComponents: 2, MaxEntries: 32}, 2, 0x30ff0},
		{"entries", coresight.WalkLimits{MaxDepth: 4, MaxComponents: 16, MaxEntries: 1}, 2, 0x20000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := walkMemory()
			visits, err := coresight.Walk(t.Context(), m, 0x10000, tt.limits)
			if !errors.Is(err, coresight.ErrWalkLimit) || len(visits) != tt.count {
				t.Fatalf("visits=%+v err=%v", visits, err)
			}
			if slices.Contains(m.reads, tt.unread) {
				t.Fatalf("read beyond limit at %x", tt.unread)
			}
		})
	}
}

func TestWalkValidationBeforeTraffic(t *testing.T) {
	m := walkMemory()
	for _, limits := range []coresight.WalkLimits{{}, {MaxDepth: -1, MaxComponents: 1, MaxEntries: 1}, {MaxComponents: -1, MaxEntries: 1}, {MaxComponents: 1, MaxEntries: -1}} {
		if err := limits.Validate(); err == nil {
			t.Fatal("invalid limits accepted")
		}
		if _, err := coresight.Walk(t.Context(), m, 0x10000, limits); err == nil {
			t.Fatal("invalid walk accepted")
		}
	}
	var nilContext context.Context
	for _, ctx := range []context.Context{nilContext, t.Context()} {
		if _, err := coresight.Walk(ctx, m, 1, walkLimits()); err == nil {
			t.Fatal("invalid argument accepted")
		}
	}
	if _, err := coresight.Walk(t.Context(), nil, 0x10000, walkLimits()); err == nil {
		t.Fatal("nil reader accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := coresight.Walk(ctx, m, 0x10000, walkLimits()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if len(m.reads) != 0 {
		t.Fatalf("reads=%v", m.reads)
	}
}

func TestWalkFailureRetainsPrefix(t *testing.T) {
	failure := errors.New("target unavailable")
	for _, failAt := range []int{1, 13, 14, 29, 31} {
		m := walkMemory()
		m.failAt = failAt
		m.failure = failure
		visits, err := coresight.Walk(t.Context(), m, 0x10000, walkLimits())
		if !errors.Is(err, failure) || len(m.reads) != failAt || len(visits) == 0 {
			t.Fatalf("at %d: visits=%+v err=%v reads=%d", failAt, visits, err, len(m.reads))
		}
	}
	m := walkMemory()
	m.words[0x10004] = 0x1001
	visits, err := coresight.Walk(t.Context(), m, 0x10000, walkLimits())
	if err == nil || len(visits) != 3 {
		t.Fatalf("malformed: visits=%+v err=%v", visits, err)
	}
}

func TestWalkLeafAndUnsupportedTable(t *testing.T) {
	m := walkMemory()
	visits, err := coresight.Walk(t.Context(), m, 0x30000, walkLimits())
	if err != nil || len(visits) != 1 {
		t.Fatalf("leaf=%+v,%v", visits, err)
	}
	m.words[0x20000+0xfc8] = 2
	visits, err = coresight.Walk(t.Context(), m, 0x20000, walkLimits())
	if err == nil || len(visits) != 1 || visits[0].Component == nil || visits[0].Err == nil {
		t.Fatalf("unsupported=%+v,%v", visits, err)
	}
}

func TestWalkCountsAbsentEntriesAndTerminators(t *testing.T) {
	m := walkMemory()
	m.words[0x10000] = 0x1002
	m.words[0x10004] = 0
	limits := walkLimits()
	limits.MaxEntries = 1
	visits, err := coresight.Walk(t.Context(), m, 0x10000, limits)
	if !errors.Is(err, coresight.ErrWalkLimit) || len(visits) != 1 || slices.Contains(m.reads, 0x10004) {
		t.Fatalf("visits=%+v err=%v", visits, err)
	}
	limits.MaxEntries = 2
	if _, err = coresight.Walk(t.Context(), m, 0x10000, limits); err != nil {
		t.Fatal(err)
	}
}

func TestWalkFullTableNeedsNoTerminator(t *testing.T) {
	m := memoryAt(0x10000, 1)
	for i := range 960 {
		m.words[0x10000+uint64(i)*4] = 0x1002
	}
	limits := walkLimits()
	limits.MaxEntries = 960
	visits, err := coresight.Walk(t.Context(), m, 0x10000, limits)
	if err != nil || len(visits) != 1 || len(m.reads) != 12+960 {
		t.Fatalf("visits=%+v err=%v reads=%d", visits, err, len(m.reads))
	}
}

func TestWalkCancellationRetainsPowerSkips(t *testing.T) {
	m := walkMemory()
	m.words[0x10000] = 0x10007
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &cancelWalkReader{componentMemory: m, cancel: cancel, address: 0x10004}
	visits, err := coresight.Walk(ctx, reader, 0x10000, walkLimits())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, coresight.ErrPowerDomain) || len(visits) != 2 {
		t.Fatalf("visits=%+v err=%v", visits, err)
	}
	if slices.Contains(m.reads, 0x40ff0) {
		t.Fatal("identified child after cancellation")
	}
}

type cancelWalkReader struct {
	*componentMemory
	cancel  context.CancelFunc
	address uint64
	after   bool
}

func (r *cancelWalkReader) ReadScalar(ctx context.Context, address uint64, size dap.TransferSize) (uint64, error) {
	if address == r.address {
		if r.after {
			word, err := r.componentMemory.ReadScalar(ctx, address, size)
			r.cancel()
			return word, err
		}
		r.cancel()
		return 0, ctx.Err()
	}
	return r.componentMemory.ReadScalar(ctx, address, size)
}

func TestWalkCancellationBeforeNextEntry(t *testing.T) {
	m := walkMemory()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &cancelWalkReader{componentMemory: m, cancel: cancel, address: 0x10fdc, after: true}
	visits, err := coresight.Walk(ctx, reader, 0x10000, walkLimits())
	if !errors.Is(err, context.Canceled) || len(visits) != 1 || visits[0].Component == nil || len(m.reads) != 12 {
		t.Fatalf("visits=%+v err=%v reads=%v", visits, err, m.reads)
	}
}
