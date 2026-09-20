package dap

import (
	"context"
	"errors"
	"fmt"
)

// APAt returns a selector for a 4 KiB aligned ADIv6 access-port base.
// Address zero is valid. The connected debug port validates its address width.
func APAt(base uint64) (APSel, error) {
	if base&0xfff != 0 {
		return APSel{}, errors.New("dap: AP base must be 4 KiB aligned")
	}
	return APSel{base: base, v2: true}, nil
}

// BaseAddress returns an ADIv6 selector's base.
// It returns an error for ADIv5 or zero selectors.
func (sel APSel) BaseAddress() (uint64, error) {
	if !sel.v2 {
		return 0, errors.New("dap: selector has no ADIv6 base address")
	}
	return sel.base, nil
}

func (sel APSel) register(addr uint8) uint16 {
	if sel.v2 {
		return 0xd00 | uint16(addr)
	}
	return uint16(addr)
}

func (dp *DebugPort) validateSelector(sel APSel) (uint64, error) {
	value, err := validateAPSel(sel)
	if err != nil {
		return 0, err
	}
	if dp.reentryID.dpidr.Version > 3 || sel.v2 != (dp.reentryID.dpidr.Version == 3) {
		return 0, errors.New("dap: AP selector does not match debug-port architecture")
	}
	if sel.v2 && value>>dp.addressBits != 0 {
		return 0, fmt.Errorf("dap: AP base %#x exceeds %d bits", value, dp.addressBits)
	}
	return value, nil
}

func (dp *DebugPort) selectAPAddress(ctx context.Context, address uint64) error {
	bits := dp.addressBits
	if dp.reentryID.dpidr.Version == 3 && bits > 32 {
		if err := dp.writeDP(ctx, SELECT1, uint32(address>>32)); err != nil {
			return err
		}
		if _, err := dp.readDP(ctx, RDBUFF); err != nil {
			return err
		}
	}
	if err := dp.writeDP(ctx, SELECT, uint32(address)&^15); err != nil {
		return err
	}
	return dp.confirmPendingSELECT(ctx)
}
