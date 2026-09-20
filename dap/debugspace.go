package dap

import (
	"context"
	"errors"
	"fmt"
)

// DebugSpace borrows the ADIv6 debug address space of a connected SW-DP.
// It owns no resources. Calls sharing its debug port must be serialized.
// Its zero value is invalid.
type DebugSpace struct{ dp *DebugPort }

// DebugSpace returns a borrowed reader without traffic. Reads require DPv3.
// ReadScalar accesses AP registers, not memory behind a MEM-AP. Those reads
// can have component-specific effects and invalidate existing MemAP clients.
// Inspect the debug space before acquiring MEM-APs.
// The caller remains responsible for releasing dp.
func (dp *DebugPort) DebugSpace() DebugSpace { return DebugSpace{dp: dp} }

func (s DebugSpace) check(ctx context.Context) error {
	if err := s.dp.requireConnected(ctx); err != nil {
		return err
	}
	if s.dp.reentryID.dpidr.Version != 3 {
		return errors.New("dap: debug address space requires DPv3")
	}
	return ctx.Err()
}

// ReadScalar reads one aligned 32-bit debug-space word. Other sizes are
// rejected before traffic. Ordinary raw-AP failure and cleanup rules apply.
func (s DebugSpace) ReadScalar(ctx context.Context, address uint64, size TransferSize) (uint64, error) {
	if err := s.check(ctx); err != nil {
		return 0, err
	}
	if size != Size32 || address&3 != 0 {
		return 0, errors.New("dap: debug-space reads require aligned 32-bit words")
	}
	sel, err := APAt(address &^ 0xfff)
	if err != nil {
		return 0, err
	}
	value, err := s.dp.ReadRawAP(ctx, sel.Address(uint16(address&0xfff)))
	return uint64(value), err
}

// ReadDebugBase reads the DP's advertised discovery root. It is in the debug
// address space, distinct from a MEM-AP's target-memory debug base. Address
// zero is valid when present is true. An absent entry returns (0, false, nil);
// malformed or failed reads never return a partially assembled address.
func (s DebugSpace) ReadDebugBase(ctx context.Context) (address uint64, present bool, err error) {
	if err := s.check(ctx); err != nil {
		return 0, false, err
	}
	low, err := s.dp.ReadDP(ctx, BASEPTR0)
	if err != nil {
		return 0, false, err
	}
	if low&0xffe != 0 {
		return 0, false, fmt.Errorf("dap: reserved bits in BASEPTR0 %#x", low)
	}
	if low&1 == 0 {
		return 0, false, nil
	}
	var high uint32
	if s.dp.addressBits > 32 {
		high, err = s.dp.ReadDP(ctx, BASEPTR1)
		if err != nil {
			return 0, false, err
		}
	}
	address = uint64(high)<<32 | uint64(low&^0xfff)
	if address>>s.dp.addressBits != 0 {
		return 0, false, errors.New("dap: debug base exceeds address width")
	}
	return address, true, nil
}
