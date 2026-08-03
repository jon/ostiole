// Package sim provides behavioral ADIv5 targets for the SWD simulator.
package sim

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
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
)

// Target models the initial SW-DP register state.
type Target struct {
	dpidr    uint32
	ctrlStat uint32
	selectDP uint32
	rdbuff   uint32
	aps      map[uint8]*accessPort
}

type accessPort struct {
	regs   map[uint8]uint32
	memory map[uint32]uint32
	memAP  bool
}

// New returns an SW-DP target with the supplied identity.
func New(dpidr uint32) *Target {
	return &Target{dpidr: dpidr, aps: make(map[uint8]*accessPort)}
}

// AddAP adds an access port with the supplied identification register.
func (t *Target) AddAP(sel uint8, idr uint32) {
	if t == nil {
		return
	}
	t.aps[sel] = &accessPort{regs: map[uint8]uint32{0xfc: idr}}
}

// AddMEMAP adds a word-readable memory access port.
func (t *Target) AddMEMAP(sel uint8, idr uint32, words map[uint32]uint32) {
	if t == nil {
		return
	}
	memory := make(map[uint32]uint32, len(words))
	for addr, value := range words {
		memory[addr] = value
	}
	t.aps[sel] = &accessPort{
		regs:   map[uint8]uint32{0xfc: idr},
		memory: memory,
		memAP:  true,
	}
}

// Read implements swd/sim.Target.
func (t *Target) Read(ctx context.Context, req swd.Request) (uint32, error) {
	if t == nil {
		return 0, errors.New("dap/sim: nil target")
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
		return t.ctrlStat, nil
	case 0x0c:
		return t.rdbuff, nil
	default:
		return 0, fmt.Errorf("dap/sim: unsupported DP read %#02x", req.Addr)
	}
}

// Write implements swd/sim.Target.
func (t *Target) Write(ctx context.Context, req swd.Request, value uint32) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
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
		t.setPower(value)
	case 0x08:
		t.selectDP = value
	default:
		return fmt.Errorf("dap/sim: unsupported DP write %#02x", req.Addr)
	}
	return nil
}

func (t *Target) readAP(req swd.Request) (uint32, error) {
	posted := t.rdbuff
	ap := t.aps[uint8(t.selectDP>>24)]
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

func (t *Target) writeAP(req swd.Request, value uint32) error {
	ap := t.aps[uint8(t.selectDP>>24)]
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

func (t *Target) apReg(req swd.Request) uint8 {
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
