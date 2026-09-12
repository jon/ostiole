package dap

// Identity records the identification register established by Connect.
// Its accessors report which register identified the port; the zero value
// contains neither DPIDR nor IDCODE.
type Identity struct {
	dpidr  DPIDRInfo
	idcode uint32
}

// DPIDR returns the decoded DPIDR when that register identified the port.
func (i Identity) DPIDR() (DPIDRInfo, bool) {
	return i.dpidr, i.dpidr.Raw != 0
}

// IDCODE returns the JTAG TAP IDCODE when that register identified the port.
func (i Identity) IDCODE() (uint32, bool) {
	return i.idcode, i.idcode != 0
}
