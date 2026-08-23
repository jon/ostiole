package dap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

const tarAutoIncrementWindow = uint64(0x400)

const blockReadChunkWords = 64

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

// ReadBlock reads an arbitrary target-memory range into buf. It retries the
// same request after WAIT while selection and framing remain known, WAIT
// cleanup succeeds, and ctx remains active. If selection, framing, or cleanup
// becomes uncertain, it returns an error and the debug port requires repair. A
// FAULT returns the contiguous byte prefix definitely obtained before the
// fault. Cancellation and transport or protocol failures can also interrupt
// the read; bytes beyond the returned prefix are left unchanged.
func (m *MemAP) ReadBlock(ctx context.Context, addr uint64, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	if m == nil || m.dp == nil {
		return 0, errors.New("dap: nil MEM-AP")
	}
	if m.epoch != m.dp.state.apGeneration {
		return 0, fmt.Errorf("dap: read target memory: MEM-AP state was invalidated by debug-port recovery")
	}
	if err := m.dp.requireConnected(); err != nil {
		return 0, err
	}
	if err := validateBlockRange(addr, len(buf), m.largeAddress); err != nil {
		return 0, fmt.Errorf("dap: read target memory: %w", err)
	}
	segments := blockSegments(addr, len(buf))
	if err := m.checkBlockSizes(ctx, segments); err != nil {
		return 0, err
	}
	n := 0
	for _, segment := range segments {
		var err error
		if segment.autoIncrement {
			n, err = m.readWordSegment(ctx, buf, segment, n)
		} else {
			n, err = m.readBlockEdge(ctx, buf, segment, n)
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (m *MemAP) checkBlockSizes(ctx context.Context, segments []blockSegment) error {
	var checked uint8
	for _, segment := range segments {
		bit := uint8(segment.size)
		if checked&bit != 0 {
			continue
		}
		checked |= bit
		if err := m.selectCSWUntilContext(ctx, segment.size, 0); err != nil {
			return fmt.Errorf("dap: read target memory: %w", err)
		}
	}
	return nil
}

func (m *MemAP) readBlockEdge(ctx context.Context, buf []byte, segment blockSegment, n int) (int, error) {
	value, err := m.readScalar(ctx, segment.address, segment.size, true)
	if err != nil {
		return n, err
	}
	m.putBlockValue(buf[segment.offset:], segment.size, value)
	width, _ := sizeBytes(segment.size)
	return n + width, nil
}

func (m *MemAP) readWordSegment(ctx context.Context, buf []byte, segment blockSegment, n int) (int, error) {
	for first := 0; first < segment.count; first += blockReadChunkWords {
		count := min(segment.count-first, blockReadChunkWords)
		address := segment.address + uint64(first*4)
		results, commitErr := m.readWordChunk(ctx, address, count)
		for i, result := range results {
			value, err := result.Value()
			if err != nil {
				if commitErr == nil {
					commitErr = err
				}
				break
			}
			m.putBlockValue(buf[segment.offset+(first+i)*4:], Size32, uint64(value))
			n += 4
		}
		if commitErr != nil {
			return n, commitErr
		}
	}
	return n, nil
}

func (m *MemAP) readWordChunk(ctx context.Context, addr uint64, count int) ([]*ReadResult, error) {
	if err := m.selectCSWUntilContext(ctx, Size32, cswIncSingle); err != nil {
		return nil, err
	}
	txn := m.dp.newTxnUntilContext()
	if m.largeAddress {
		m.restoreTARHI = true
		txn.writeAP(m.sel, memAPTARHI, uint32(addr>>32))
	}
	m.restoreTAR = true
	txn.writeAP(m.sel, memAPTAR, uint32(addr))
	results := make([]*ReadResult, count)
	for i := range results {
		results[i] = txn.readAPSequential(m.sel, memAPDRW)
	}
	return results, txn.Commit(ctx)
}

func (m *MemAP) putBlockValue(dst []byte, size TransferSize, value uint64) {
	var order binary.ByteOrder = binary.LittleEndian
	if m.bigEndian {
		order = binary.BigEndian
	}
	switch size {
	case Size8:
		dst[0] = byte(value)
	case Size16:
		order.PutUint16(dst, uint16(value))
	case Size32:
		order.PutUint32(dst, uint32(value))
	}
}
