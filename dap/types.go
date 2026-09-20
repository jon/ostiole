package dap

import "fmt"

// DPRegister identifies one logical debug-port register. Registers which
// share a physical SWD offset remain distinct values.
type DPRegister uint16

// Debug-port registers. DPIDR1, BASEPTR0, BASEPTR1, and SELECT1 require DPv3.
const (
	DPIDR DPRegister = iota + 1
	ABORT
	CTRLSTAT
	DLCR
	TARGETID
	DLPIDR
	EVENTSTAT
	SELECT
	RESEND
	RDBUFF
	IDCODE
	DPIDR1
	BASEPTR0
	BASEPTR1
	SELECT1
)

type dpRegisterInfo struct {
	name            string
	offset          uint8
	bank            uint8
	bankIndependent bool
	readable        bool
	writable        bool
	minVersion      uint8
}

func describeDPRegister(reg DPRegister) (dpRegisterInfo, bool) {
	switch reg {
	case IDCODE:
		return dpRegisterInfo{name: "IDCODE", bankIndependent: true, readable: true}, true
	case DPIDR:
		return dpRegisterInfo{name: "DPIDR", offset: 0x00, bankIndependent: true, readable: true}, true
	case ABORT:
		return dpRegisterInfo{name: "ABORT", offset: 0x00, bankIndependent: true, writable: true}, true
	case CTRLSTAT:
		return dpRegisterInfo{name: "CTRL/STAT", offset: 0x04, readable: true, writable: true}, true
	case SELECT:
		return dpRegisterInfo{name: "SELECT", offset: 0x08, bankIndependent: true, writable: true}, true
	case RESEND:
		return dpRegisterInfo{name: "RESEND", offset: 0x08, bankIndependent: true, readable: true}, true
	case RDBUFF:
		return dpRegisterInfo{name: "RDBUFF", offset: 0x0c, bankIndependent: true, readable: true}, true
	default:
		return describeBankedDPRegister(reg)
	}
}

func describeBankedDPRegister(reg DPRegister) (dpRegisterInfo, bool) {
	switch reg {
	case DPIDR1:
		return dpRegisterInfo{name: "DPIDR1", bank: 1, readable: true, minVersion: 3}, true
	case BASEPTR0:
		return dpRegisterInfo{name: "BASEPTR0", bank: 2, readable: true, minVersion: 3}, true
	case BASEPTR1:
		return dpRegisterInfo{name: "BASEPTR1", bank: 3, readable: true, minVersion: 3}, true
	case SELECT1:
		return dpRegisterInfo{name: "SELECT1", offset: 4, bank: 5, writable: true, minVersion: 3}, true
	case DLCR:
		return dpRegisterInfo{name: "DLCR", offset: 0x04, bank: 1, readable: true, writable: true, minVersion: 1}, true
	case TARGETID:
		return dpRegisterInfo{name: "TARGETID", offset: 0x04, bank: 2, readable: true, minVersion: 2}, true
	case DLPIDR:
		return dpRegisterInfo{name: "DLPIDR", offset: 0x04, bank: 3, readable: true, minVersion: 2}, true
	case EVENTSTAT:
		return dpRegisterInfo{name: "EVENTSTAT", offset: 0x04, bank: 4, readable: true, minVersion: 2}, true
	default:
		return dpRegisterInfo{}, false
	}
}

func dpRegisterOffset(reg DPRegister) uint8 {
	info, _ := describeDPRegister(reg)
	return info.offset
}

// String returns the architectural register name.
func (r DPRegister) String() string {
	if info, ok := describeDPRegister(r); ok {
		return info.name
	}
	return fmt.Sprintf("DPRegister(%#04x)", uint16(r))
}

// DPIDRInfo contains the structural fields of a DPIDR value.
type DPIDRInfo struct {
	Raw      uint32
	Revision uint8
	Part     uint8
	Minimal  bool
	Version  uint8
	Designer uint16
}

// DecodeDPIDR validates and decodes a debug-port identification register.
func DecodeDPIDR(value uint32) (DPIDRInfo, error) {
	if value&1 == 0 {
		return DPIDRInfo{}, fmt.Errorf("dap: DPIDR %#08x does not have its constant-one bit set", value)
	}
	return DPIDRInfo{
		Raw:      value,
		Revision: uint8(value >> 28),
		Part:     uint8(value >> 20 & 0xff),
		Minimal:  value>>16&1 != 0,
		Version:  uint8(value >> 12 & 0x0f),
		Designer: uint16(value >> 1 & 0x7ff),
	}, nil
}
