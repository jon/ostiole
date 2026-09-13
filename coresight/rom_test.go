package coresight_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/jon/ostiole/coresight"
)

func romComponent(class uint8, format uint32) coresight.Component {
	return coresight.Component{Base: 0x100000000, CIDR: 0xb105000d | uint32(class)<<12, DEVARCH: 0x47700af7, DEVID: format}
}

func TestROMTableLayout(t *testing.T) {
	for _, tt := range []struct {
		class  uint8
		format uint32
		count  int
	}{{1, 0, 960}, {9, 0, 512}, {9, 1, 256}} {
		c := romComponent(tt.class, tt.format)
		table, err := c.ROMTable()
		if err != nil || table.EntryCount() != tt.count {
			t.Fatalf("layout=%+v,%v", table, err)
		}
		m := &componentMemory{words: map[uint64]uint32{}}
		stride := uint64(4)
		if tt.format == 1 {
			stride = 8
		}
		address := c.Base + uint64(tt.count-1)*stride
		m.words[address] = 0x1003
		if stride == 8 {
			m.words[address+4] = 0
		}
		entry, err := table.ReadEntry(t.Context(), m, tt.count-1)
		if err != nil || entry.Base != c.Base+0x1000 || !entry.Present {
			t.Fatalf("last=%+v,%v", entry, err)
		}
		before := len(m.reads)
		for _, i := range []int{-1, tt.count} {
			if _, err = table.ReadEntry(t.Context(), m, i); err == nil {
				t.Fatal("invalid index accepted")
			}
		}
		if len(m.reads) != before {
			t.Fatal("invalid index reached memory")
		}
	}
}

func TestROMTableRejectsInvalidIdentity(t *testing.T) {
	for _, edit := range []func(*coresight.Component){
		func(c *coresight.Component) { c.CIDR = 0 }, func(c *coresight.Component) { c.Base++ },
		func(c *coresight.Component) { c.DEVID = 2 },
		func(c *coresight.Component) { c.DEVARCH |= 1 << 16 }, func(c *coresight.Component) { c.DEVARCH &^= 1 << 20 },
		func(c *coresight.Component) { c.DEVARCH ^= 1 << 21 }, func(c *coresight.Component) { c.DEVARCH++ },
	} {
		c := romComponent(9, 0)
		edit(&c)
		if _, err := c.ROMTable(); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := romComponent(0xe, 0)
	if _, err := c.ROMTable(); !errors.Is(err, coresight.ErrNotROMTable) {
		t.Fatalf("non-table error=%v", err)
	}
	m := memoryAt(0, 1)
	var table coresight.ROMTable
	if _, err := table.ReadEntry(t.Context(), m, 0); err == nil || len(m.reads) != 0 {
		t.Fatal("zero table read memory")
	}
}

func TestROMEntryFormats(t *testing.T) {
	for _, tt := range []struct {
		name                         string
		class                        uint8
		format                       uint32
		raw                          uint64
		base                         uint64
		present, end, power, invalid bool
	}{
		{name: "class1 end", class: 1, end: true},
		{name: "class1 absent", class: 1, raw: 0x1002},
		{name: "class1 positive", class: 1, raw: 0x1003, base: 0x100001000, present: true},
		{name: "class1 negative", class: 1, raw: 0xfffff003, base: 0xfffff000, present: true},
		{name: "power domain zero", class: 1, raw: 0x1007, base: 0x100001000, present: true, power: true},
		{name: "format zero", class: 1, raw: 0x1001, invalid: true},
		{name: "all ones is malformed", class: 1, raw: 0xffffffff, invalid: true},
		{name: "reserved", class: 1, raw: 0x1203, invalid: true},
		{name: "unqualified power", class: 1, raw: 0x1013, invalid: true},
		{name: "self", class: 1, raw: 3, invalid: true},
		{name: "class9 end", class: 9, end: true},
		{name: "class9 malformed end", class: 9, raw: 0x1000, invalid: true},
		{name: "class9 reserved presence", class: 9, raw: 0x1001, invalid: true},
		{name: "class9 absent unknown bits", class: 9, raw: 0xfffffffe},
		{name: "class9 present", class: 9, raw: 0x1003, base: 0x100001000, present: true},
		{name: "64-bit positive", class: 9, format: 1, raw: 0x200001003, base: 0x300001000, present: true},
		{name: "64-bit negative", class: 9, format: 1, raw: 0xfffffffffffff003, base: 0xfffff000, present: true},
		{name: "64-bit malformed end high", class: 9, format: 1, raw: 0x100000000, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := romComponent(tt.class, tt.format)
			table, err := c.ROMTable()
			if err != nil {
				t.Fatal(err)
			}
			m := &componentMemory{words: map[uint64]uint32{c.Base: uint32(tt.raw), c.Base + 4: uint32(tt.raw >> 32)}}
			got, err := table.ReadEntry(t.Context(), m, 0)
			if (err != nil) != tt.invalid {
				t.Fatalf("entry=%+v,%v", got, err)
			}
			if tt.invalid {
				if got != (coresight.ROMEntry{}) {
					t.Fatal("partial entry returned")
				}
				return
			}
			if got.Raw != tt.raw || got.Base != tt.base || got.Present != tt.present || got.End != tt.end || got.PowerIDValid != tt.power {
				t.Fatalf("entry=%+v", got)
			}
			reads := 1
			if tt.format == 1 {
				reads = 2
			}
			if len(m.reads) != reads {
				t.Fatalf("reads=%v", m.reads)
			}
		})
	}
}

func TestROMEntryAddressBounds(t *testing.T) {
	for _, tt := range []struct {
		base, raw uint64
		valid     bool
	}{
		{0, 0xfffffffffffff003, false}, {math.MaxUint64 - 0xfff, 0x1003, false},
		{0x8000000000000000, 0x8000000000000003, true},
		{0, 0x7ffffffffffff003, true},
	} {
		c := romComponent(9, 1)
		c.Base = tt.base
		table, err := c.ROMTable()
		if err != nil {
			t.Fatal(err)
		}
		m := &componentMemory{words: map[uint64]uint32{c.Base: uint32(tt.raw), c.Base + 4: uint32(tt.raw >> 32)}}
		if _, err = table.ReadEntry(t.Context(), m, 0); (err == nil) != tt.valid {
			t.Fatalf("base=%x raw=%x err=%v", tt.base, tt.raw, err)
		}
	}
}

func TestROMEntryReadFailure(t *testing.T) {
	c := romComponent(9, 1)
	table, err := c.ROMTable()
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("memory failure")
	for _, failAt := range []int{1, 2} {
		m := &componentMemory{words: map[uint64]uint32{c.Base: 0, c.Base + 4: 0}, failAt: failAt, failure: failure}
		got, err := table.ReadEntry(t.Context(), m, 0)
		if !errors.Is(err, failure) || got != (coresight.ROMEntry{}) || len(m.reads) != failAt {
			t.Fatalf("entry=%+v,%v reads=%v", got, err, m.reads)
		}
	}
	m := memoryAt(c.Base, 1)
	var nilContext context.Context
	if _, err = table.ReadEntry(nilContext, m, 0); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err = table.ReadEntry(t.Context(), nil, 0); err == nil {
		t.Fatal("nil reader accepted")
	}
}

func TestROMEntryCancellation(t *testing.T) {
	c := romComponent(9, 1)
	table, err := c.ROMTable()
	if err != nil {
		t.Fatal(err)
	}
	for _, before := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		m := &componentMemory{words: map[uint64]uint32{c.Base: 3}, cancel: cancel}
		if before {
			cancel()
		}
		entry, err := table.ReadEntry(ctx, m, 0)
		cancel()
		want := 1
		if before {
			want = 0
		}
		if !errors.Is(err, context.Canceled) || entry != (coresight.ROMEntry{}) || len(m.reads) != want {
			t.Fatalf("entry=%+v,%v reads=%v", entry, err, m.reads)
		}
	}
}
