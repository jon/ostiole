package sim

import "errors"

// SetDebugWord sets a read-only word in the ADIv6 debug address space. It can
// supply ROM entries or component identities independently of MEM-AP memory.
// Configure DPIDR1 before adding words. Duplicate addresses replace prior data.
func (t *Target) SetDebugWord(address uint64, value uint32) error {
	if t == nil || t.dpidr>>12&15 != 3 {
		return errors.New("dap/sim: debug words require DPv3")
	}
	if address&3 != 0 || !t.validDebugAddress(address) {
		return errors.New("dap/sim: invalid debug word address")
	}
	if t.debugWords == nil {
		t.debugWords = make(map[uint64]uint32)
	}
	t.debugWords[address] = value
	return nil
}

func (t *Target) validDebugAddress(address uint64) bool {
	switch bits := t.dpIDBanks[1] & 0x7f; bits {
	case 12, 20, 32, 40, 48, 52:
		return address>>bits == 0
	default:
		return false
	}
}
