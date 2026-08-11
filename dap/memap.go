package dap

import (
	"context"
	"errors"
	"fmt"
)

// MEM-AP registers used by the first target-word reader.
const (
	APCSW APReg = 0x00
	APTAR APReg = 0x04
	APDRW APReg = 0x0c
)

const (
	memAPClass = uint32(0x08)
	cswSize    = uint32(0x07)
	cswAddrInc = uint32(0x30)
	cswSize32  = uint32(0x02)
)

// MemAP reads aligned target words through one memory access port.
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
func NewMemAP(ctx context.Context, dp *DebugPort, sel APSel) (*MemAP, error) {
	idr, err := dp.ReadAP(ctx, sel, APIDR)
	if err != nil {
		return nil, err
	}
	if idr == 0 {
		return nil, fmt.Errorf("dap: AP %d is absent", sel)
	}
	if idr>>13&0x0f != memAPClass {
		return nil, fmt.Errorf("dap: AP %d is not a MEM-AP", sel)
	}
	csw, err := dp.ReadAP(ctx, sel, APCSW)
	if err != nil {
		return nil, err
	}
	tar, err := dp.ReadAP(ctx, sel, APTAR)
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

// ReadWord reads one aligned 32-bit target word.
func (m *MemAP) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	if m == nil || m.dp == nil {
		return 0, errors.New("dap: nil MEM-AP")
	}
	if m.epoch != m.dp.state.apGeneration {
		return 0, errors.New("dap: read target word: MEM-AP state was invalidated by debug-port recovery")
	}
	if addr&3 != 0 {
		return 0, fmt.Errorf("dap: unaligned target word address %#08x", addr)
	}
	m.restoreCSW = true
	if err := m.dp.WriteAP(ctx, m.sel, APCSW, m.csw); err != nil {
		return 0, err
	}
	m.restoreTAR = true
	if err := m.dp.WriteAP(ctx, m.sel, APTAR, addr); err != nil {
		return 0, err
	}
	return m.dp.ReadAP(ctx, m.sel, APDRW)
}

// Release restores the CSW and TAR values changed by target reads.
//
// Failed restoration remains pending so Release can be retried. The debug port
// must remain connected until Release succeeds.
func (m *MemAP) Release(ctx context.Context) error {
	if m == nil || m.dp == nil {
		return nil
	}
	releaseCtx := ctx
	if m.dp.state.session != sessionIdle && m.dp.state.response != responseSimple {
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.Background(), waitRecoveryTimeout)
		defer cancel()
		if err := m.dp.reenter(releaseCtx); err != nil {
			return fmt.Errorf("dap: restore SWD protocol state for MEM-AP release: %w", err)
		}
	}
	if m.restoreEpoch != m.dp.state.apGeneration {
		m.restoreTAR = true
		m.restoreCSW = true
		m.restoreEpoch = m.dp.state.apGeneration
	}
	var tarErr, cswErr error
	if m.restoreTAR {
		tarErr = m.dp.WriteAP(releaseCtx, m.sel, APTAR, m.savedTAR)
		if tarErr != nil {
			tarErr = fmt.Errorf("dap: restore MEM-AP TAR: %w", tarErr)
		} else {
			m.restoreTAR = false
		}
	}
	if m.restoreCSW {
		cswErr = m.dp.WriteAP(releaseCtx, m.sel, APCSW, m.savedCSW)
		if cswErr != nil {
			cswErr = fmt.Errorf("dap: restore MEM-AP CSW: %w", cswErr)
		} else {
			m.restoreCSW = false
		}
	}
	return errors.Join(tarErr, cswErr)
}
