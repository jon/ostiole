package dap

import (
	"math"
	"testing"
)

func TestBlockSegments(t *testing.T) {
	tests := []struct {
		name   string
		addr   uint64
		length int
		want   []blockSegment
	}{
		{name: "empty"},
		{name: "one byte", length: 1, want: []blockSegment{{size: Size8, count: 1}}},
		{name: "aligned halfword", length: 2, want: []blockSegment{{size: Size16, count: 1}}},
		{name: "aligned three-byte tail", length: 3, want: []blockSegment{{size: Size16, count: 1}, {offset: 2, address: 2, size: Size8, count: 1}}},
		{name: "unaligned mixed access", addr: 1, length: 10, want: []blockSegment{
			{address: 1, size: Size8, count: 1},
			{offset: 1, address: 2, size: Size16, count: 1},
			{offset: 3, address: 4, size: Size32, count: 1, autoIncrement: true},
			{offset: 7, address: 8, size: Size16, count: 1},
			{offset: 9, address: 10, size: Size8, count: 1},
		}},
		{name: "split at TAR window", addr: 0x3f8, length: 16, want: []blockSegment{
			{address: 0x3f8, size: Size32, count: 2, autoIncrement: true},
			{offset: 8, address: 0x400, size: Size32, count: 2, autoIncrement: true},
		}},
		{name: "unaligned TAR transition", addr: 0x3fd, length: 12, want: []blockSegment{
			{address: 0x3fd, size: Size8, count: 1},
			{offset: 1, address: 0x3fe, size: Size16, count: 1},
			{offset: 3, address: 0x400, size: Size32, count: 2, autoIncrement: true},
			{offset: 11, address: 0x408, size: Size8, count: 1},
		}},
		{name: "multiple TAR windows", length: 2056, want: []blockSegment{
			{size: Size32, count: 256, autoIncrement: true},
			{offset: 1024, address: 0x400, size: Size32, count: 256, autoIncrement: true},
			{offset: 2048, address: 0x800, size: Size32, count: 2, autoIncrement: true},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := blockSegments(test.addr, test.length)
			if len(got) != len(test.want) {
				t.Fatalf("blockSegments(%#x, %d) = %+v, want %+v", test.addr, test.length, got, test.want)
			}
			for i := range test.want {
				if got[i] != test.want[i] {
					t.Errorf("segment %d = %+v, want %+v", i, got[i], test.want[i])
				}
			}
		})
	}
}

func TestValidateBlockRange(t *testing.T) {
	tests := []struct {
		name         string
		addr         uint64
		length       int
		largeAddress bool
		wantErr      bool
	}{
		{name: "empty", addr: math.MaxUint64},
		{name: "32-bit end", addr: math.MaxUint32 - 1, length: 2},
		{name: "32-bit overflow", addr: math.MaxUint32, length: 2, wantErr: true},
		{name: "large address", addr: 1 << 32, length: 4096, largeAddress: true},
		{name: "64-bit overflow", addr: math.MaxUint64 - 1, length: 3, largeAddress: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateBlockRange(test.addr, test.length, test.largeAddress)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateBlockRange() error = %v", err)
			}
		})
	}
}

func FuzzBlockSegments(f *testing.F) {
	f.Add(uint64(0), uint16(0))
	f.Add(uint64(1), uint16(10))
	f.Add(uint64(0x3fd), uint16(12))
	f.Add(uint64(0x1_000003f8), uint16(2056))
	f.Fuzz(func(t *testing.T, addr uint64, rawLength uint16) {
		length := int(rawLength % 8193)
		if uint64(length) > math.MaxUint64-addr {
			return
		}
		segments := blockSegments(addr, length)
		nextOffset := 0
		nextAddress := addr
		for i, segment := range segments {
			if segment.offset != nextOffset || segment.address != nextAddress || segment.count <= 0 {
				t.Fatalf("segment %d = %+v after offset %d address %#x", i, segment, nextOffset, nextAddress)
			}
			width, err := sizeBytes(segment.size)
			if err != nil || segment.address&uint64(width-1) != 0 {
				t.Fatalf("segment %d has invalid size or alignment: %+v", i, segment)
			}
			if segment.autoIncrement {
				last := segment.address + uint64(segment.count*width-1)
				if segment.size != Size32 || segment.address>>10 != last>>10 {
					t.Fatalf("segment %d crosses a TAR window: %+v", i, segment)
				}
			} else if segment.count != 1 {
				t.Fatalf("non-incrementing segment %d has count %d", i, segment.count)
			}
			nextOffset += segment.count * width
			nextAddress += uint64(segment.count * width)
		}
		if nextOffset != length || nextAddress != addr+uint64(length) {
			t.Fatalf("segments end at offset %d address %#x, want %d and %#x", nextOffset, nextAddress, length, addr+uint64(length))
		}
		if length == 0 && segments != nil {
			t.Fatalf("blockSegments(%#x, 0) = %+v, want nil", addr, segments)
		}
	})
}
