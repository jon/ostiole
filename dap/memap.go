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
	dp  *DebugPort
	sel APSel
	csw uint32
}

// NewMemAP validates sel and prepares its 32-bit, non-incrementing CSW value.
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
	csw &^= cswSize | cswAddrInc
	csw |= cswSize32
	return &MemAP{dp: dp, sel: sel, csw: csw}, nil
}

// ReadWord reads one aligned 32-bit target word.
func (m *MemAP) ReadWord(ctx context.Context, addr uint32) (uint32, error) {
	if m == nil || m.dp == nil {
		return 0, errors.New("dap: nil MEM-AP")
	}
	if addr&3 != 0 {
		return 0, fmt.Errorf("dap: unaligned target word address %#08x", addr)
	}
	if err := m.dp.WriteAP(ctx, m.sel, APCSW, m.csw); err != nil {
		return 0, err
	}
	if err := m.dp.WriteAP(ctx, m.sel, APTAR, addr); err != nil {
		return 0, err
	}
	return m.dp.ReadAP(ctx, m.sel, APDRW)
}
