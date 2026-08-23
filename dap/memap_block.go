package dap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

const tarAutoIncrementWindow = uint64(0x400)

const blockReadChunkWords = 64

const blockWriteChunkWords = 64

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

// ReadBlock reads an arbitrary target-memory range into buf. Like other DAP
// operations, it retries the same physical request after a clean WAIT until the
// debug port's configured limit is reached or the operation context ends. If
// selection, framing, or cleanup becomes uncertain, it returns an error and the
// debug port requires repair. A FAULT returns the contiguous byte prefix
// definitely obtained before the fault. Cancellation and transport or protocol
// failures can also interrupt the read; bytes beyond the returned prefix are
// left unchanged. If the MEM-AP does not accept single address increment,
// ReadBlock writes TAR before each word instead.
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
	if err := m.checkBlockSizes(ctx, segments, "read"); err != nil {
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

func (m *MemAP) checkBlockSizes(ctx context.Context, segments []blockSegment, operation string) error {
	var checked uint8
	for _, segment := range segments {
		bit := uint8(segment.size)
		if checked&bit != 0 {
			continue
		}
		checked |= bit
		var err error
		if operation == "read" {
			err = m.selectCSWUntilContext(ctx, segment.size, 0)
		} else {
			err = m.selectCSWUntilContext(ctx, segment.size, 0)
		}
		if err != nil {
			return fmt.Errorf("dap: %s target memory: %w", operation, err)
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
		if errors.Is(err, errAddressIncrementUnsupported) {
			return m.readWordsWithoutIncrement(ctx, addr, count)
		}
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

func (m *MemAP) readWordsWithoutIncrement(ctx context.Context, addr uint64, count int) ([]*ReadResult, error) {
	if err := m.selectCSWUntilContext(ctx, Size32, 0); err != nil {
		return nil, err
	}
	results := make([]*ReadResult, count)
	for i := range results {
		txn := m.scalarTxnUntilContext(addr + uint64(i*4))
		results[i] = txn.readAP(m.sel, memAPDRW)
		if err := txn.Commit(ctx); err != nil {
			return results[:i+1], err
		}
	}
	return results, nil
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

// WriteBlock writes an arbitrary target-memory range from buf. It returns the
// contiguous byte prefix whose RDBUFF completion requests were accepted. If
// the current chunk might have reached memory, the error wraps ErrIndeterminate
// and WriteBlock does not retry that chunk. An indeterminate chunk invalidates
// the MemAP; Release remains available to restore its saved state. WriteBlock
// retries the same physical request after a clean WAIT until the operation
// context ends or the debug port's configured limit is reached. It never
// replays an accepted write; if the RDBUFF completion request returns WAIT, it
// retries only that request. If the MEM-AP does not accept single address
// increment, WriteBlock writes TAR before each word.
func (m *MemAP) WriteBlock(ctx context.Context, addr uint64, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	if m == nil || m.dp == nil {
		return 0, errors.New("dap: nil MEM-AP")
	}
	if m.epoch != m.dp.state.apGeneration {
		return 0, errors.New("dap: write target memory: MEM-AP state was invalidated by debug-port recovery")
	}
	if err := m.dp.requireConnected(); err != nil {
		return 0, err
	}
	if err := validateBlockRange(addr, len(buf), m.largeAddress); err != nil {
		return 0, fmt.Errorf("dap: write target memory: %w", err)
	}
	segments := blockSegments(addr, len(buf))
	if err := m.checkBlockSizes(ctx, segments, "write"); err != nil {
		return 0, err
	}
	n := 0
	for _, segment := range segments {
		var err error
		n, err = m.writeBlockSegment(ctx, buf, segment, n)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (m *MemAP) writeBlockSegment(ctx context.Context, buf []byte, segment blockSegment, n int) (int, error) {
	chunkLimit := 1
	addrInc := uint32(0)
	if segment.autoIncrement {
		chunkLimit = blockWriteChunkWords
		addrInc = cswIncSingle
	}
	width, _ := sizeBytes(segment.size)
	for first := 0; first < segment.count; first += chunkLimit {
		count := min(segment.count-first, chunkLimit)
		address := segment.address + uint64(first*width)
		values := make([]uint32, count)
		for i := range values {
			offset := segment.offset + (first+i)*width
			values[i] = m.blockWriteValue(buf[offset:], address+uint64(i*width), segment.size)
		}
		confirmed, err := m.writeBlockChunk(ctx, address, segment.size, addrInc, values)
		n += confirmed * width
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (m *MemAP) writeBlockChunk(ctx context.Context, addr uint64, size TransferSize, addrInc uint32, values []uint32) (int, error) {
	if err := m.selectCSWUntilContext(ctx, size, addrInc); err != nil {
		if addrInc == cswIncSingle && errors.Is(err, errAddressIncrementUnsupported) {
			return m.writeWordsWithoutIncrement(ctx, addr, size, values)
		}
		return 0, err
	}
	return m.writeBlockValues(ctx, addr, values)
}

func (m *MemAP) writeWordsWithoutIncrement(ctx context.Context, addr uint64, size TransferSize, values []uint32) (int, error) {
	if err := m.selectCSWUntilContext(ctx, size, 0); err != nil {
		return 0, err
	}
	width, _ := sizeBytes(size)
	confirmed := 0
	for i := range values {
		count, err := m.writeBlockValues(ctx, addr+uint64(i*width), values[i:i+1])
		confirmed += count
		if err != nil {
			return confirmed, err
		}
	}
	return confirmed, nil
}

func (m *MemAP) writeBlockValues(ctx context.Context, addr uint64, values []uint32) (int, error) {
	if m.largeAddress {
		m.restoreTARHI = true
		if err := m.writeAPUntilContext(ctx, memAPTARHI, uint32(addr>>32)); err != nil {
			return 0, err
		}
	}
	m.restoreTAR = true
	if err := m.writeAPUntilContext(ctx, memAPTAR, uint32(addr)); err != nil {
		return 0, err
	}
	writeTxn := m.dp.newTxnUntilContext()
	writeTxn.writeAPSequence(m.sel, memAPDRW, values)
	generation := m.dp.state.apGeneration
	err := writeTxn.Commit(ctx)
	if err == nil {
		return len(values), nil
	}
	accepted := writeTxn.ops[0].accepted
	if accepted == len(values) && errors.Is(err, swd.ErrParity) && !errors.Is(err, ErrIndeterminate) {
		return len(values), err
	}
	if accepted > 0 && (len(values) != 1 || !faultReportsWriteDataError(err)) {
		err = m.markBlockWriteIndeterminate(err, generation)
	}
	return 0, err
}

func (m *MemAP) writeAPUntilContext(ctx context.Context, addr uint8, value uint32) error {
	txn := m.dp.newTxnUntilContext()
	txn.writeAP(m.sel, addr, value)
	return txn.Commit(ctx)
}

func (m *MemAP) markBlockWriteIndeterminate(err error, generation uint64) error {
	if errors.Is(err, ErrIndeterminate) {
		return err
	}
	if m.dp.state.apGeneration == generation {
		m.dp.state.invalidateAP()
	}
	return errors.Join(err, ErrIndeterminate)
}

func (m *MemAP) blockWriteValue(src []byte, addr uint64, size TransferSize) uint32 {
	var order binary.ByteOrder = binary.LittleEndian
	if m.bigEndian {
		order = binary.BigEndian
	}
	switch size {
	case Size8:
		return uint32(src[0]) << m.laneShift(addr, size)
	case Size16:
		return uint32(order.Uint16(src)) << m.laneShift(addr, size)
	default:
		return order.Uint32(src)
	}
}
