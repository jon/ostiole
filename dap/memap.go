package dap

import (
	"context"
	"errors"
	"fmt"
)

const (
	memAPCSW = uint8(0x00)
	memAPTAR = uint8(0x04)
	memAPDRW = uint8(0x0c)
)

const (
	memAPClass = uint8(0x08)
	cswSize    = uint32(0x07)
	cswAddrInc = uint32(0x30)
	cswSize32  = uint32(0x02)
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
	restoreCSW   bool
	restoreTAR   bool
}

// NewMemAP validates sel and saves the state changed by target reads.
// The debug port must be connected and must not have cleanup pending.
func NewMemAP(ctx context.Context, dp *DebugPort, sel APSel) (*MemAP, error) {
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
	csw, err := dp.readAP(ctx, sel, memAPCSW)
	if err != nil {
		return nil, err
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
	if !m.restoreCSW && !m.restoreTAR {
		return nil
	}
	releaseCtx, cancel, err := m.prepareRelease(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if m.restoreEpoch != m.dp.state.apGeneration {
		m.restoreTAR = true
		m.restoreCSW = true
		m.restoreEpoch = m.dp.state.apGeneration
	}
	var tarErr, cswErr error
	if m.restoreTAR {
		tarErr = m.dp.writeAP(releaseCtx, m.sel, memAPTAR, m.savedTAR)
		if tarErr != nil {
			tarErr = fmt.Errorf("dap: restore MEM-AP TAR: %w", tarErr)
		} else {
			m.restoreTAR = false
		}
	}
	if m.restoreCSW {
		cswErr = m.dp.writeAP(releaseCtx, m.sel, memAPCSW, m.savedCSW)
		if cswErr != nil {
			cswErr = fmt.Errorf("dap: restore MEM-AP CSW: %w", cswErr)
		} else {
			m.restoreCSW = false
		}
	}
	return errors.Join(tarErr, cswErr)
}

func (m *MemAP) prepareRelease(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if m.dp.state.session == sessionIdle {
		return nil, nil, errors.New("dap: SW-DP is not connected")
	}
	if m.dp.state.response == responseSimple {
		return ctx, func() {}, nil
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), waitRecoveryTimeout)
	if err := m.dp.reenter(releaseCtx); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("dap: restore SWD protocol state for MEM-AP release: %w", err)
	}
	return releaseCtx, cancel, nil
}
