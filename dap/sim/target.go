// Package sim provides behavioral ADIv5 targets for the SWD simulator.
package sim

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/dap"
	swdsim "github.com/jon/ostiole/swd/sim"
)

const (
	clearStickyCompare  = uint32(1 << 1)
	clearStickyError    = uint32(1 << 2)
	clearWriteDataError = uint32(1 << 3)
	clearStickyOverrun  = uint32(1 << 4)

	stickyOverrun  = uint32(1 << 1)
	stickyCompare  = uint32(1 << 4)
	stickyError    = uint32(1 << 5)
	writeDataError = uint32(1 << 7)

	debugPowerRequest  = uint32(1 << 28)
	debugPowerAck      = uint32(1 << 29)
	systemPowerRequest = uint32(1 << 30)
	systemPowerAck     = uint32(1 << 31)

	dlcrTurnaroundMask = uint32(3 << 8)
)

// Target models the initial SW-DP register state.
type Target struct {
	dpidr     uint32
	ctrlStat  uint32
	dpBanks   [16]uint32
	dpBankSet [16]bool
	selectDP  uint32
	rdbuff    uint32
	aps       map[dap.APSel]*accessPort
}

type accessPort struct {
	regs   map[uint8]uint32
	memory map[uint32]uint32
	memAP  bool
}

// New returns an SW-DP target with the supplied identity.
func New(dpidr uint32) *Target {
	return &Target{dpidr: dpidr, aps: make(map[dap.APSel]*accessPort)}
}

// SetDPRegister sets one simulated banked debug-port register.
func (t *Target) SetDPRegister(reg dap.DPRegister, value uint32) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	var bank uint8
	switch reg {
	case dap.DLCR:
		bank = 1
	case dap.TARGETID:
		bank = 2
	case dap.DLPIDR:
		bank = 3
	case dap.EVENTSTAT:
		bank = 4
	default:
		return fmt.Errorf("dap/sim: %s has no banked fixture", reg)
	}
	if reg == dap.DLCR && value&dlcrTurnaroundMask != 0 {
		return errors.New("dap/sim: variable SWD turnaround is not modeled")
	}
	if t.dpBankSet[bank] {
		return fmt.Errorf("dap/sim: %s is already configured", reg)
	}
	t.dpBanks[bank] = value
	t.dpBankSet[bank] = true
	return nil
}

// AddAP adds an access port with the supplied identification register. It
// rejects a zero APIDR and an existing selector.
func (t *Target) AddAP(sel dap.APSel, idr uint32) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	if idr == 0 {
		return errors.New("dap/sim: APIDR must be nonzero")
	}
	selection, err := sel.Value()
	if err != nil {
		return err
	}
	if _, ok := t.aps[sel]; ok {
		return fmt.Errorf("dap/sim: AP %d is already configured", selection)
	}
	t.aps[sel] = &accessPort{regs: map[uint8]uint32{0xfc: idr}}
	return nil
}

// AddMEMAP adds a word-readable memory access port. It rejects a non-MEM-AP
// identity, an existing selector, and unaligned target-word addresses.
func (t *Target) AddMEMAP(sel dap.APSel, idr uint32, words map[uint32]uint32) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	if idr == 0 || dap.DecodeAPIDR(idr).Class != 8 {
		return errors.New("dap/sim: MEM-AP requires a nonzero class-8 APIDR")
	}
	selection, err := sel.Value()
	if err != nil {
		return err
	}
	if _, ok := t.aps[sel]; ok {
		return fmt.Errorf("dap/sim: AP %d is already configured", selection)
	}
	memory := make(map[uint32]uint32, len(words))
	for addr, value := range words {
		if addr&3 != 0 {
			return fmt.Errorf("dap/sim: unaligned target-word address %#08x", addr)
		}
		memory[addr] = value
	}
	t.aps[sel] = &accessPort{
		regs:   map[uint8]uint32{0xfc: idr},
		memory: memory,
		memAP:  true,
	}
	return nil
}

// Read implements swd/sim.Target.
func (t *Target) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	if t == nil {
		return 0, errors.New("dap/sim: nil target")
	}
	if err := validateRequest(req, true); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if req.AP {
		return t.readAP(req)
	}
	switch req.Addr {
	case 0x00:
		return t.dpidr, nil
	case 0x04:
		bank := uint8(t.selectDP & 0x0f)
		if bank == 0 {
			return t.ctrlStat, nil
		}
		return t.dpBanks[bank], nil
	case 0x08:
		return t.rdbuff, nil
	case 0x0c:
		return t.rdbuff, nil
	default:
		return 0, fmt.Errorf("dap/sim: unsupported DP read %#02x", req.Addr)
	}
}

// Write implements swd/sim.Target.
func (t *Target) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	if err := validateRequest(req, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.AP {
		return t.writeAP(req, value)
	}
	switch req.Addr {
	case 0x00:
		t.clearSticky(value)
	case 0x04:
		bank := uint8(t.selectDP & 0x0f)
		switch bank {
		case 0:
			t.setPower(value)
		case 1:
			if value&dlcrTurnaroundMask != 0 {
				return errors.New("dap/sim: variable SWD turnaround is not modeled")
			}
			t.dpBanks[bank] = value
		}
	case 0x08:
		t.selectDP = value
	default:
		return fmt.Errorf("dap/sim: unsupported DP write %#02x", req.Addr)
	}
	return nil
}

func validateRequest(req swdsim.Request, read bool) error {
	if req.Addr&3 != 0 || req.Addr > 0x0c {
		return fmt.Errorf("dap/sim: invalid request address %#02x", req.Addr)
	}
	if req.Read != read {
		return errors.New("dap/sim: invalid request direction")
	}
	return nil
}

func (t *Target) readAP(req swdsim.Request) (uint32, error) {
	posted := t.rdbuff
	ap := t.aps[dap.NewAPSel(uint8(t.selectDP>>24))]
	if ap == nil {
		t.rdbuff = 0
		return posted, nil
	}
	reg := t.apReg(req)
	if ap.memAP && reg == 0x0c {
		csw := ap.regs[0x00]
		if csw&0x07 != 2 || csw&0x30 != 0 {
			return 0, errors.New("dap/sim: DRW read requires 32-bit, non-incrementing CSW")
		}
		t.rdbuff = ap.memory[ap.regs[0x04]]
	} else {
		t.rdbuff = ap.regs[reg]
	}
	return posted, nil
}

func (t *Target) writeAP(req swdsim.Request, value uint32) error {
	ap := t.aps[dap.NewAPSel(uint8(t.selectDP>>24))]
	if ap == nil {
		return nil
	}
	reg := t.apReg(req)
	if ap.memAP && reg == 0x0c {
		return errors.New("dap/sim: target-memory writes are not modeled")
	}
	ap.regs[reg] = value
	return nil
}

func (t *Target) apReg(req swdsim.Request) uint8 {
	bank := uint8(t.selectDP>>4) & 0x0f
	return bank<<4 | req.Addr
}

func (t *Target) clearSticky(value uint32) {
	if value&clearStickyCompare != 0 {
		t.ctrlStat &^= stickyCompare
	}
	if value&clearStickyError != 0 {
		t.ctrlStat &^= stickyError
	}
	if value&clearWriteDataError != 0 {
		t.ctrlStat &^= writeDataError
	}
	if value&clearStickyOverrun != 0 {
		t.ctrlStat &^= stickyOverrun
	}
}

func (t *Target) setPower(value uint32) {
	sticky := t.ctrlStat & (stickyOverrun | stickyCompare |
		stickyError | writeDataError)
	requests := value & (debugPowerRequest | systemPowerRequest)
	t.ctrlStat = sticky | requests
	if requests&debugPowerRequest != 0 {
		t.ctrlStat |= debugPowerAck
	}
	if requests&systemPowerRequest != 0 {
		t.ctrlStat |= systemPowerAck
	}
}
