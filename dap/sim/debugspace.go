package sim

import "errors"

// SetDebugWord sets a read-only word in the ADIv6 debug address space. It can
// supply ROM entries or component identities independently of MEM-AP memory.
// Configure DPIDR1 before adding words. Duplicate addresses replace prior data.
func (t *Target) SetDebugWord(address uint64, value uint32) error {
	if t == nil || t.dpidr>>12&15 != 3 {
		return errors.New("dap/sim: debug words require DPv3")
	}
	bits := t.dpIDBanks[1] & 0x7f
	if bits == 0 || bits > 64 || address&3 != 0 || address>>bits != 0 {
		return errors.New("dap/sim: invalid debug word address")
	}
	if t.debugWords == nil {
		t.debugWords = make(map[uint64]uint32)
	}
	t.debugWords[address] = value
	return nil
}
