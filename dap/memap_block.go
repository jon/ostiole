package dap

import "fmt"

const tarAutoIncrementWindow = uint64(0x400)

type blockSegment struct {
	offset        int
	address       uint64
	size          TransferSize
	count         int
	autoIncrement bool
}

func blockSegments(addr uint64, length int) []blockSegment {
	if length <= 0 {
		return nil
	}
	var segments []blockSegment
	offset := 0
	appendSegment := func(size TransferSize, count int, autoIncrement bool) {
		segments = append(segments, blockSegment{offset: offset, address: addr, size: size, count: count, autoIncrement: autoIncrement})
		width, _ := sizeBytes(size)
		step := width * count
		offset += step
		addr += uint64(step)
	}
	for offset < length && addr&3 != 0 {
		size := Size8
		if addr&1 == 0 && length-offset >= 2 {
			size = Size16
		}
		appendSegment(size, 1, false)
	}
	for words := (length - offset) / 4; words > 0; {
		windowWords := int((tarAutoIncrementWindow - addr&(tarAutoIncrementWindow-1)) / 4)
		count := min(words, windowWords)
		appendSegment(Size32, count, true)
		words -= count
	}
	for offset < length {
		size := Size8
		if addr&1 == 0 && length-offset >= 2 {
			size = Size16
		}
		appendSegment(size, 1, false)
	}
	return segments
}

func validateBlockRange(addr uint64, length int, largeAddress bool) error {
	if length <= 0 {
		return nil
	}
	width := uint64(length)
	if width-1 > ^addr {
		return fmt.Errorf("dap: address range at %#x overflows", addr)
	}
	if !largeAddress && addr+width-1 > uint64(^uint32(0)) {
		return fmt.Errorf("dap: address range at %#x requires CFG.LA", addr)
	}
	return nil
}
