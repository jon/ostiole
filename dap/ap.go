package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

// APSel identifies one access port. Its zero value is invalid; construct a
// selector with NewAPSel.
type APSel struct {
	index uint16
}

// NewAPSel returns the selector for one ADIv5 access port.
func NewAPSel(value uint8) APSel {
	return APSel{index: uint16(value) + 1}
}

// Value returns the architectural selector value. The zero APSel returns an
// error.
func (sel APSel) Value() (uint8, error) {
	if sel.index == 0 {
		return 0, errors.New("dap: zero APSel is invalid")
	}
	return uint8(sel.index - 1), nil
}

// APAddress identifies one register on one access port. Its zero value is
// invalid; derive an address from an APSel with Address.
type APAddress struct {
	sel   APSel
	value uint8
}

// Address returns a complete ADIv5 access-port register address without
// sending traffic. The operation which uses the address reports an error if
// value is not four-byte aligned or sel is the zero value.
func (sel APSel) Address(value uint8) APAddress {
	return APAddress{sel: sel, value: value}
}

const apIDRAddress = uint8(0xfc)

// APIDRInfo contains the fields of an ADIv5 access-port identification
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
	if err := dp.requireConnected(); err != nil {
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
	if err := dp.requireConnected(); err != nil {
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
	_, value, err := dp.readAPEffect(ctx, sel, addr)
	return value, err
}

func (dp *DebugPort) readAPEffect(ctx context.Context, sel APSel, addr uint8) (bool, uint32, error) {
	if err := validateRawAPAddress(addr, false); err != nil {
		return false, 0, err
	}
	if err := dp.selectAP(ctx, sel, addr); err != nil {
		return false, 0, err
	}
	_, err := dp.transfer(ctx, apTransferRequest(addr&0x0c, true), 0)
	if err != nil {
		possible := !requestWasRejected(err) && !requestWasNotSent(err) && !errors.Is(err, swd.ErrFault)
		return possible, 0, fmt.Errorf("dap: post raw AP read at %#02x: %w", addr, err)
	}
	value, err := dp.readDP(ctx, RDBUFF)
	return true, value, err
}

// WriteRawAP writes the register at one complete access-port address and waits
// for completion. A write that completes or might have completed invalidates
// existing MemAP values. The caller must understand the selected AP class and
// restore any state the write changes. Writing a MEM-AP data register can
// write target memory. The address must be four-byte aligned. The debug port
// must be connected and have no cleanup pending.
func (dp *DebugPort) WriteRawAP(ctx context.Context, addr APAddress, value uint32) error {
	if err := dp.requireConnected(); err != nil {
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
	_, err := dp.writeAPEffect(ctx, sel, addr, value)
	return err
}

func (dp *DebugPort) writeAPEffect(ctx context.Context, sel APSel, addr uint8, value uint32) (bool, error) {
	if err := validateRawAPAddress(addr, true); err != nil {
		return false, err
	}
	if err := dp.selectAP(ctx, sel, addr); err != nil {
		return false, err
	}
	_, err := dp.transfer(ctx, apTransferRequest(addr&0x0c, false), value)
	if err != nil {
		possible := !requestWasRejected(err) && !requestWasNotSent(err) && !errors.Is(err, swd.ErrFault)
		return possible, fmt.Errorf("dap: write raw AP register at %#02x: %w", addr, err)
	}
	if _, err := dp.readDP(ctx, RDBUFF); err != nil {
		return !faultReportsWriteDataError(err), fmt.Errorf("dap: complete raw AP write at %#02x: %w", addr, err)
	}
	return true, nil
}

func (dp *DebugPort) selectAP(ctx context.Context, sel APSel, addr uint8) error {
	selection, err := validateAPSel(sel)
	if err != nil {
		return err
	}
	value := uint32(selection)<<24 | uint32(addr&0xf0)
	if !dp.state.selectDP.valid || dp.state.selectDP.value != value {
		if err := dp.writeDP(ctx, SELECT, value); err != nil {
			return err
		}
	}
	return dp.confirmPendingSELECT(ctx)
}

func validateAPSel(sel APSel) (uint8, error) {
	return sel.Value()
}

func validateAPAddress(addr APAddress, write bool) (uint8, error) {
	if _, err := validateAPSel(addr.sel); err != nil {
		return 0, err
	}
	if err := validateRawAPAddress(addr.value, write); err != nil {
		return 0, err
	}
	return addr.value, nil
}

func validateRawAPAddress(addr uint8, write bool) error {
	if addr&3 != 0 {
		return fmt.Errorf("dap: unaligned AP address %#02x", addr)
	}
	if write && addr == apIDRAddress {
		return errors.New("dap: APIDR is read-only")
	}
	return nil
}

func (dp *DebugPort) requireConnected() error {
	if dp == nil || dp.conn == nil || dp.state.session == sessionIdle {
		return errors.New("dap: SW-DP is not connected")
	}
	if dp.state.session == sessionRepairRequired {
		return dp.repairPendingError()
	}
	return nil
}
