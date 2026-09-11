package jtag

import "errors"

// TAPSpec describes an expected instruction length and reset register. Its
// zero value is invalid. Construct it with IDCODE or Bypass.
type TAPSpec struct {
	ir    int
	reset ObservedTAP
}

// IDCODE describes a TAP with a 2..64-bit IR and a nonzero odd reset IDCODE.
// Matching is exact, including revision bits; all ones is a terminator, not an ID.
func IDCODE(irBits int, id uint32) (TAPSpec, error) {
	if irBits < 2 || irBits > 64 || id&1 == 0 || id == ^uint32(0) {
		return TAPSpec{}, errors.New("jtag: invalid IR length or IDCODE")
	}
	return TAPSpec{ir: irBits, reset: ObservedTAP{IDCODE: id}}, nil
}

// Bypass describes a TAP whose reset-selected register is a one-bit bypass.
// It does not assert an identity for the device behind that bypass.
func Bypass(irBits int) (TAPSpec, error) {
	if irBits < 2 || irBits > 64 {
		return TAPSpec{}, errors.New("jtag: invalid IR length")
	}
	return TAPSpec{ir: irBits, reset: ObservedTAP{Bypass: true}}, nil
}

// IRBits returns the expected instruction length, or zero for an invalid spec.
func (s TAPSpec) IRBits() int { return s.ir }

// ResetRegister returns a detached expected reset-register observation.
func (s TAPSpec) ResetRegister() ObservedTAP { return s.reset }

// Layout describes TAPs in scan-out order, nearest TDO first. NewChain copies it.
// Matching reset registers and IR captures validates a supplied layout; it
// cannot prove a physical identity for bypass entries or infer IR boundaries.
type Layout []TAPSpec
