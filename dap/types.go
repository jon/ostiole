package dap

import "fmt"

// DPReg identifies a debug-port register address.
type DPReg uint8

// DPAddress identifies an ADIv5 debug-port register and its SELECT bank.
// Bank is significant for registers at offset 0x04.
type DPAddress struct {
	Addr DPReg
	Bank uint8
}

// Debug-port register addresses used by the initial SW-DP client.
const (
	ABORT    DPReg = 0x00
	DPIDR    DPReg = 0x00
	CTRLSTAT DPReg = 0x04
	SELECT   DPReg = 0x08
	RDBUFF   DPReg = 0x0c
)

// Named ADIv5 debug-port register addresses.
var (
	ABORTAddr     = DPAddress{Addr: ABORT}
	DPIDRAddr     = DPAddress{Addr: DPIDR}
	CTRLSTATAddr  = DPAddress{Addr: CTRLSTAT}
	DLCRAddr      = DPAddress{Addr: CTRLSTAT, Bank: 1}
	TARGETIDAddr  = DPAddress{Addr: CTRLSTAT, Bank: 2}
	DLPIDRAddr    = DPAddress{Addr: CTRLSTAT, Bank: 3}
	EVENTSTATAddr = DPAddress{Addr: CTRLSTAT, Bank: 4}
	SELECTAddr    = DPAddress{Addr: SELECT}
	RESENDAddr    = DPAddress{Addr: SELECT}
	RDBUFFAddr    = DPAddress{Addr: RDBUFF}
)

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
