package dap

import (
	"context"
	"errors"
	"fmt"
)

// APSel identifies one access port. Its zero value is invalid; construct a
// selector with NewAPSel or APAt.
type APSel struct {
	index uint16
	base  uint64
	v2    bool
}

// NewAPSel returns the selector for one ADIv5 access port.
func NewAPSel(value uint8) APSel {
	return APSel{index: uint16(value) + 1}
}

// Value returns an ADIv5 selector value.
// It returns an error for ADIv6 or zero selectors.
func (sel APSel) Value() (uint8, error) {
	if sel.index == 0 || sel.v2 {
		return 0, errors.New("dap: selector has no ADIv5 APSEL value")
	}
	return uint8(sel.index - 1), nil
}

// APAddress identifies one register on one access port. Its zero value is
// invalid; derive an address from an APSel with Address.
type APAddress struct {
	sel   APSel
	value uint16
}

// Address returns a complete access-port register address without
// sending traffic. The operation which uses the address reports an error if
// value is not four-byte aligned, exceeds the register window (256 bytes for
// ADIv5, 4 KiB for ADIv6), or sel is invalid.
func (sel APSel) Address(value uint16) APAddress {
	return APAddress{sel: sel, value: value}
}

const apIDRAddress = uint8(0xfc)

// APIDRInfo contains the fields of an Arm access-port identification
// register.
type APIDRInfo struct {
	Raw      uint32
	Revision uint8
	Designer uint16
	Class    uint8
	Variant  uint8
	Type     uint8
}

// DecodeAPIDR decodes an access-port identification register.
func DecodeAPIDR(value uint32) APIDRInfo {
	return APIDRInfo{
		Raw:      value,
		Revision: uint8(value >> 28),
		Designer: uint16(value >> 17 & 0x7ff),
		Class:    uint8(value >> 13 & 0x0f),
		Variant:  uint8(value >> 4 & 0x0f),
		Type:     uint8(value & 0x0f),
	}
}

// ReadAPIDR reads and decodes the identification register of one access port.
// The debug port must be connected and have no cleanup pending.
func (dp *DebugPort) ReadAPIDR(ctx context.Context, sel APSel) (APIDRInfo, error) {
	if err := dp.requireConnected(ctx); err != nil {
		return APIDRInfo{}, err
	}
	value, err := dp.readAP(ctx, sel, apIDRAddress)
	if err != nil {
		return APIDRInfo{}, err
	}
	return DecodeAPIDR(value), nil
}

// ReadRawAP reads the register at one complete access-port address through the
// posted pipeline. A read that completes or might have completed invalidates
// existing MemAP values. The caller must understand the selected AP class and
// restore any state the read changes. The address must be four-byte aligned.
// The debug port must be connected and have no cleanup pending.
func (dp *DebugPort) ReadRawAP(ctx context.Context, addr APAddress) (uint32, error) {
	if err := dp.requireConnected(ctx); err != nil {
		return 0, err
	}
	value, err := validateAPAddress(addr, false)
	if err != nil {
		return 0, err
	}
	generation := dp.state.apGeneration
	possible, result, err := dp.readAPEffect(ctx, addr.sel, value)
	if possible && dp.state.apGeneration == generation {
		dp.state.invalidateAP()
	}
	return result, err
}

func (dp *DebugPort) readAP(ctx context.Context, sel APSel, addr uint8) (uint32, error) {
	_, value, err := dp.readAPEffect(ctx, sel, sel.register(addr))
	return value, err
}

func (dp *DebugPort) readAPEffect(ctx context.Context, sel APSel, addr uint16) (bool, uint32, error) {
	if _, err := validateAPAddress(APAddress{sel: sel, value: addr}, false); err != nil {
		return false, 0, err
	}
	if err := dp.selectAP(ctx, sel, addr); err != nil {
		return false, 0, err
	}
	if dp.jtag != nil {
		result := dp.jtag.accessAP(ctx, apTransferRequest(uint8(addr&0x0c), true), 0)
		return result.outcome != transferUnsent && result.outcome != transferRejected, result.data, jtagResultError(result)
	}
	return dp.conn.readAP(ctx, uint8(addr&0x0c))
}

// WriteRawAP writes the register at one complete access-port address and waits
// for completion. A write that completes or might have completed invalidates
// existing MemAP values. The caller must understand the selected AP class and
// restore any state the write changes. Writing a MEM-AP data register can
// write target memory. The address must be four-byte aligned. The debug port
// must be connected and have no cleanup pending.
// A JTAG write with uncertain completion also reports ErrIndeterminate.
func (dp *DebugPort) WriteRawAP(ctx context.Context, addr APAddress, value uint32) error {
	if err := dp.requireConnected(ctx); err != nil {
		return err
	}
	address, err := validateAPAddress(addr, true)
	if err != nil {
		return err
	}
	generation := dp.state.apGeneration
	possible, err := dp.writeAPEffect(ctx, addr.sel, address, value)
	if possible && dp.state.apGeneration == generation {
		dp.state.invalidateAP()
	}
	return err
}

func (dp *DebugPort) writeAP(ctx context.Context, sel APSel, addr uint8, value uint32) error {
	_, err := dp.writeAPEffect(ctx, sel, sel.register(addr), value)
	return err
}

func (dp *DebugPort) writeAPEffect(ctx context.Context, sel APSel, addr uint16, value uint32) (bool, error) {
	if _, err := validateAPAddress(APAddress{sel: sel, value: addr}, true); err != nil {
		return false, err
	}
	if err := dp.selectAP(ctx, sel, addr); err != nil {
		return false, err
	}
	if dp.jtag != nil {
		result := dp.jtag.accessAP(ctx, apTransferRequest(uint8(addr&0x0c), false), value)
		return result.outcome != transferUnsent && result.outcome != transferRejected, jtagResultError(result)
	}
	return dp.conn.writeAP(ctx, uint8(addr&0x0c), value)
}

func (dp *DebugPort) selectAP(ctx context.Context, sel APSel, addr uint16) error {
	selection, err := dp.validateSelector(sel)
	if err != nil {
		return err
	}
	if sel.v2 {
		return dp.selectAPAddress(ctx, selection+uint64(addr))
	}
	value := uint32(selection)<<24 | uint32(addr&0xf0)
	if !dp.state.selectDP.valid || dp.state.selectDP.value != value {
		if err := dp.writeDP(ctx, SELECT, value); err != nil {
			return err
		}
	}
	return dp.confirmPendingSELECT(ctx)
}

func validateAPSel(sel APSel) (uint64, error) {
	if sel.v2 {
		return sel.base, nil
	}
	index, err := sel.Value()
	return uint64(index), err
}

func validateAPAddress(addr APAddress, write bool) (uint16, error) {
	if _, err := validateAPSel(addr.sel); err != nil {
		return 0, err
	}
	limit := uint16(0xff)
	if addr.sel.v2 {
		limit = 0xfff
	}
	if addr.value > limit {
		return 0, fmt.Errorf("dap: AP register offset %#x exceeds %#x", addr.value, limit)
	}
	if write && addr.value == addr.sel.register(apIDRAddress) {
		return 0, errors.New("dap: APIDR is read-only")
	}
	if err := validateRawAPAddress(addr.value, false); err != nil {
		return 0, err
	}
	return addr.value, nil
}

func validateRawAPAddress(addr uint16, write bool) error {
	if addr&3 != 0 {
		return fmt.Errorf("dap: unaligned AP address %#02x", addr)
	}
	if write && addr == uint16(apIDRAddress) {
		return errors.New("dap: APIDR is read-only")
	}
	return nil
}

func (dp *DebugPort) requireConnected(ctx context.Context) error {
	if ctx == nil {
		return errors.New("dap: nil context")
	}
	if !dp.bound() || dp.state.session == sessionIdle {
		return errors.New("dap: debug port is not connected")
	}
	if dp.state.session == sessionRepairRequired {
		return dp.repairPendingError()
	}
	return nil
}
