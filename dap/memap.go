package dap

import (
	"context"
	"errors"
	"fmt"
)

const (
	memAPCSW   = uint8(0x00)
	memAPTAR   = uint8(0x04)
	memAPTARHI = uint8(0x08)
	memAPDRW   = uint8(0x0c)
	memAPCFG   = uint8(0xf4)
)

const (
	memAPClass = uint8(0x08)
	cswSize    = uint32(0x07)
	cswAddrInc = uint32(0x30)
	cswSize32  = uint32(0x02)

	cfgBigEndian = uint32(1 << 0)
	cfgLargeAddr = uint32(1 << 1)
	cfgLargeData = uint32(1 << 2)
)

// TransferSize names the width of one MEM-AP scalar transfer. Its zero value
// is invalid.
type TransferSize uint8

const (
	Size8 TransferSize = 1 << iota
	Size16
	Size32
	Size64
)

// MemAP reads and writes aligned scalar values through one memory access port.
//
// Its methods and all uses of its DebugPort must be serialized. Release the
// MemAP before releasing its DebugPort. Reads and writes change volatile
// MEM-AP registers; Release restores the saved values. If a failed Size64
// transfer might have started its first DRW access, release the MemAP and then
// the DebugPort before reconnecting.
type MemAP struct {
	dp           *DebugPort
	sel          APSel
	epoch        uint64
	restoreEpoch uint64
	csw          uint32
	savedCSW     uint32
	savedTAR     uint32
	savedTARHI   uint32
	bigEndian    bool
	largeAddress bool
	largeData    bool
	largePending bool
	restoreCSW   bool
	restoreTAR   bool
	restoreTARHI bool
}

// OpenMemAP validates sel and saves the state changed by target-memory access.
// It performs AP traffic. A returned MemAP must be paired with MemAP.Release.
// The debug port must be connected and must not have cleanup pending.
func OpenMemAP(ctx context.Context, dp *DebugPort, sel APSel) (*MemAP, error) {
	selection, err := validateAPSel(sel)
	if err != nil {
		return nil, err
	}
	idr, err := dp.ReadAPIDR(ctx, sel)
	if err != nil {
		return nil, err
	}
	if idr.Raw == 0 {
		return nil, fmt.Errorf("dap: AP %d is absent", selection)
	}
	if idr.Class != memAPClass {
		return nil, fmt.Errorf("dap: AP %d is not a MEM-AP", selection)
	}
	cfg, err := dp.readAP(ctx, sel, memAPCFG)
	if err != nil {
		return nil, err
	}
	csw, err := dp.readAP(ctx, sel, memAPCSW)
	if err != nil {
		return nil, err
	}
	var tarhi uint32
	if cfg&cfgLargeAddr != 0 {
		tarhi, err = dp.readAP(ctx, sel, memAPTARHI)
		if err != nil {
			return nil, err
		}
	}
	tar, err := dp.readAP(ctx, sel, memAPTAR)
	if err != nil {
		return nil, err
	}
	configuredCSW := csw &^ (cswSize | cswAddrInc)
	configuredCSW |= cswSize32
	return &MemAP{
		dp:           dp,
		sel:          sel,
		epoch:        dp.state.apGeneration,
		restoreEpoch: dp.state.apGeneration,
		csw:          configuredCSW,
		savedCSW:     csw,
		savedTAR:     tar,
		savedTARHI:   tarhi,
		bigEndian:    cfg&cfgBigEndian != 0,
		largeAddress: cfg&cfgLargeAddr != 0,
		largeData:    cfg&cfgLargeData != 0,
	}, nil
}

// ReadWord reads one aligned 32-bit target word.
func (m *MemAP) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	value, err := m.ReadScalar(ctx, uint64(addr), Size32)
	return uint32(value), err
}

// ReadScalar performs one aligned, sized target-memory read. The returned
// value is right-justified regardless of target byte order or address lane.
func (m *MemAP) ReadScalar(ctx context.Context, addr uint64, size TransferSize) (uint64, error) {
	if err := m.checkScalar(addr, size, "read"); err != nil {
		return 0, err
	}
	if err := m.selectSize(ctx, size); err != nil {
		return 0, err
	}
	txn := m.scalarTxn(addr)
	low := txn.readAP(m.sel, memAPDRW)
	var high *ReadResult
	if size == Size64 {
		high = txn.readAP(m.sel, memAPDRW)
		m.largePending = true
	}
	generation := m.dp.state.apGeneration
	if err := txn.Commit(ctx); err != nil {
		if size == Size64 {
			m.requireLargeDataCleanup(generation)
		}
		return 0, err
	}
	m.largePending = false
	lowValue, err := low.Value()
	if err != nil {
		return 0, err
	}
	if high != nil {
		highValue, err := high.Value()
		if err != nil {
			return 0, err
		}
		return uint64(lowValue) | uint64(highValue)<<32, nil
	}
	return uint64(lowValue>>m.laneShift(addr, size)) & sizeMask(size), nil
}

// WriteScalar performs one aligned, sized target-memory write. Sub-word values
// are placed in the address lane selected by the MEM-AP byte order. It returns
// only after the AP completion barrier succeeds.
func (m *MemAP) WriteScalar(ctx context.Context, addr uint64, size TransferSize, value uint64) error {
	if err := m.checkScalar(addr, size, "write"); err != nil {
		return err
	}
	if value&^sizeMask(size) != 0 {
		width, _ := sizeBytes(size)
		return fmt.Errorf("dap: write target memory: value %#x exceeds %d bits", value, width*8)
	}
	if err := m.selectSize(ctx, size); err != nil {
		return err
	}
	txn := m.scalarTxn(addr)
	if size == Size64 {
		txn.writeAP(m.sel, memAPDRW, uint32(value))
		txn.writeAP(m.sel, memAPDRW, uint32(value>>32))
		m.largePending = true
	} else {
		data := uint32(value) << m.laneShift(addr, size)
		txn.writeAP(m.sel, memAPDRW, data)
	}
	generation := m.dp.state.apGeneration
	err := txn.Commit(ctx)
	if err == nil {
		m.largePending = false
	} else if size == Size64 {
		m.requireLargeDataCleanup(generation)
	}
	return err
}

func (m *MemAP) requireLargeDataCleanup(generation uint64) {
	if m.dp.state.apGeneration == generation {
		m.dp.state.invalidateAP()
	}
	m.dp.state.beginRepair()
}

func (m *MemAP) checkScalar(addr uint64, size TransferSize, operation string) error {
	if m == nil || m.dp == nil {
		return errors.New("dap: nil MEM-AP")
	}
	if m.epoch != m.dp.state.apGeneration {
		return fmt.Errorf("dap: %s target memory: MEM-AP state was invalidated by debug-port recovery", operation)
	}
	if err := m.dp.requireConnected(); err != nil {
		return err
	}
	if err := validateScalarAccess(addr, size, m.largeAddress); err != nil {
		return fmt.Errorf("dap: %s target memory: %w", operation, err)
	}
	if size == Size64 && !m.largeData {
		return fmt.Errorf("dap: %s target memory: Size64 requires CFG.LD", operation)
	}
	return nil
}

func (m *MemAP) selectSize(ctx context.Context, size TransferSize) error {
	encoding, err := transferSizeEncoding(size)
	if err != nil {
		return err
	}
	txn := m.dp.NewTxn()
	m.restoreCSW = true
	txn.writeAP(m.sel, memAPCSW, m.csw&^cswSize|encoding)
	selected := txn.readAP(m.sel, memAPCSW)
	if err := txn.Commit(ctx); err != nil {
		return err
	}
	m.largePending = false
	value, err := selected.Value()
	if err != nil {
		return err
	}
	if value&cswSize != encoding {
		width, _ := sizeBytes(size)
		return fmt.Errorf("dap: MEM-AP does not support %d-bit transfers", width*8)
	}
	return nil
}

func transferSizeEncoding(size TransferSize) (uint32, error) {
	switch size {
	case Size8:
		return 0, nil
	case Size16:
		return 1, nil
	case Size32:
		return 2, nil
	case Size64:
		return 3, nil
	default:
		return 0, fmt.Errorf("invalid transfer size %d", size)
	}
}

func (m *MemAP) scalarTxn(addr uint64) *Txn {
	txn := m.dp.NewTxn()
	if m.largeAddress {
		m.restoreTARHI = true
		txn.writeAP(m.sel, memAPTARHI, uint32(addr>>32))
	}
	m.restoreTAR = true
	txn.writeAP(m.sel, memAPTAR, uint32(addr))
	return txn
}

func (m *MemAP) laneShift(addr uint64, size TransferSize) uint {
	width, _ := sizeBytes(size)
	lane := int(addr & 3)
	if m.bigEndian {
		lane = 4 - width - lane
	}
	return uint(lane * 8)
}

func sizeMask(size TransferSize) uint64 {
	switch size {
	case Size8:
		return 0xff
	case Size16:
		return 0xffff
	case Size32:
		return 0xffffffff
	default:
		return ^uint64(0)
	}
}

// Release restores the CSW, TAR, and, when present, TARHI values changed by
// target-memory access.
//
// Release remains available while debug-port cleanup is pending. Failed
// restoration remains pending so the method can be retried. Call it before
// DebugPort.Release.
func (m *MemAP) Release(ctx context.Context) error {
	if m == nil || m.dp == nil {
		return nil
	}
	if !m.restoreCSW && !m.restoreTAR && !m.restoreTARHI {
		return nil
	}
	releaseCtx, cancel, err := m.prepareRelease(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if m.restoreEpoch != m.dp.state.apGeneration {
		m.restoreTAR = true
		m.restoreTARHI = m.largeAddress
		m.restoreCSW = true
		m.restoreEpoch = m.dp.state.apGeneration
	}
	if m.largePending {
		if err := m.restoreRegister(releaseCtx, &m.restoreCSW, memAPCSW, m.savedCSW, "CSW"); err != nil {
			return err
		}
		m.largePending = false
	}
	tarhiErr := m.restoreRegister(releaseCtx, &m.restoreTARHI, memAPTARHI, m.savedTARHI, "TARHI")
	tarErr := m.restoreRegister(releaseCtx, &m.restoreTAR, memAPTAR, m.savedTAR, "TAR")
	cswErr := m.restoreRegister(releaseCtx, &m.restoreCSW, memAPCSW, m.savedCSW, "CSW")
	return errors.Join(tarhiErr, tarErr, cswErr)
}

func (m *MemAP) restoreRegister(ctx context.Context, pending *bool, reg uint8, value uint32, name string) error {
	if !*pending {
		return nil
	}
	if err := m.dp.writeAP(ctx, m.sel, reg, value); err != nil {
		return fmt.Errorf("dap: restore MEM-AP %s: %w", name, err)
	}
	*pending = false
	return nil
}

func validateScalarAccess(addr uint64, size TransferSize, largeAddress bool) error {
	width, err := sizeBytes(size)
	if err != nil {
		return err
	}
	if addr&uint64(width-1) != 0 {
		return fmt.Errorf("dap: address %#x is not %d-byte aligned", addr, width)
	}
	if uint64(width-1) > ^addr {
		return fmt.Errorf("dap: address range at %#x overflows", addr)
	}
	if !largeAddress && addr+uint64(width-1) > uint64(^uint32(0)) {
		return fmt.Errorf("dap: address range at %#x requires CFG.LA", addr)
	}
	return nil
}

func sizeBytes(size TransferSize) (int, error) {
	switch size {
	case Size8:
		return 1, nil
	case Size16:
		return 2, nil
	case Size32:
		return 4, nil
	case Size64:
		return 8, nil
	default:
		return 0, fmt.Errorf("dap: invalid MEM-AP transfer size %d", size)
	}
}

func (m *MemAP) prepareRelease(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if m.dp.state.session == sessionIdle {
		return nil, nil, errors.New("dap: SW-DP is not connected")
	}
	if m.dp.state.responseKnown() {
		return ctx, func() {}, nil
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), waitRecoveryTimeout)
	if err := m.dp.reenter(releaseCtx); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("dap: restore SWD protocol state for MEM-AP release: %w", err)
	}
	return releaseCtx, cancel, nil
}
