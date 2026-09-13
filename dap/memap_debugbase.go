package dap

import (
	"context"
	"fmt"
)

const (
	memAPBASE   = uint8(0xf8)
	memAPBASEHI = uint8(0xf0)
)

// ReadDebugBase reads this MEM-AP's advertised debug entry address. A present
// entry names the identification page of a component, often a ROM table; it
// does not establish that the component is accessible or identify its class.
// Address zero is valid when present is true. Absence returns (0, false, nil).
// Errors return (0, false, err), never an address assembled from partial reads.
//
// The method decodes ADIv5 and legacy BASE formats, reading the upper word only
// for a present ADIv5 entry with CFG.LA set. Malformed encodings, including
// legacy formats with CFG.LA, return errors. It preserves usable MEM-AP state
// on success and performs no target-memory access. Ordinary DAP recovery and
// cleanup rules apply after failure. Calls sharing the debug port must be serialized.
func (m *MemAP) ReadDebugBase(ctx context.Context) (address uint64, present bool, err error) {
	if err := m.checkActive(ctx, "read debug base"); err != nil {
		return 0, false, err
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	low, err := m.dp.readAP(ctx, m.sel, memAPBASE)
	if err != nil {
		return 0, false, fmt.Errorf("dap: read MEM-AP BASE: %w", err)
	}
	present, err = debugBasePresent(low, m.largeAddress)
	if err != nil || !present {
		return 0, false, err
	}
	var high uint32
	if m.largeAddress {
		high, err = m.dp.readAP(ctx, m.sel, memAPBASEHI)
		if err != nil {
			return 0, false, fmt.Errorf("dap: read MEM-AP BASE upper word: %w", err)
		}
	}
	return uint64(high)<<32 | uint64(low&0xfffff000), true, nil
}

func debugBasePresent(low uint32, large bool) (bool, error) {
	if low == 0xffffffff && !large {
		return false, nil
	}
	if low&2 == 0 {
		if large || low&0xfff != 0 {
			return false, fmt.Errorf("dap: invalid legacy MEM-AP BASE %#08x (CFG.LA=%t)", low, large)
		}
		return true, nil
	}
	if low&0xffc != 0 {
		return false, fmt.Errorf("dap: reserved bits set in MEM-AP BASE %#08x", low)
	}
	return low&1 != 0, nil
}
