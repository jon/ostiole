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

// MemAP reads aligned target words through one memory access port.
//
// Its methods and all uses of its DebugPort must be serialized. Release the
// MemAP before releasing its DebugPort.
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
	restoreCSW   bool
	restoreTAR   bool
	restoreTARHI bool
}

// OpenMemAP validates sel and saves the state changed by target reads. It
// performs AP traffic. A returned MemAP must be paired with MemAP.Release. The
// debug port must be connected and must not have cleanup pending.
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

// ReadWord reads one aligned 32-bit target word. Debug-port recovery
// invalidates further reads through an existing MemAP.
func (m *MemAP) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	if m == nil || m.dp == nil {
		return 0, errors.New("dap: nil MEM-AP")
	}
	if m.epoch != m.dp.state.apGeneration {
		return 0, errors.New("dap: read target word: MEM-AP state was invalidated by debug-port recovery")
	}
	if err := m.dp.requireConnected(); err != nil {
		return 0, err
	}
	if addr&3 != 0 {
		return 0, fmt.Errorf("dap: unaligned target word address %#08x", addr)
	}
	m.restoreCSW = true
	if err := m.dp.writeAP(ctx, m.sel, memAPCSW, m.csw); err != nil {
		return 0, err
	}
	m.restoreTAR = true
	if err := m.dp.writeAP(ctx, m.sel, memAPTAR, addr); err != nil {
		return 0, err
	}
	return m.dp.readAP(ctx, m.sel, memAPDRW)
}

// Release restores the CSW and TAR values changed by target reads.
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
