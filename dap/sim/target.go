// Package sim provides behavioral ADIv5 targets for the SWD simulator.
package sim

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
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
	overrunDetect  = uint32(1 << 0)

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
	regs       map[uint8]uint32
	memory     map[uint64]byte
	memAP      bool
	sizes      uint8
	largePhase largeDataPhase
	largeWord  uint32
}

type largeDataPhase uint8

const (
	largeDataIdle largeDataPhase = iota
	largeDataRead
	largeDataWrite
)

// New returns an SW-DP target with the supplied identity.
func New(dpidr uint32) *Target {
	return &Target{dpidr: dpidr, aps: make(map[dap.APSel]*accessPort)}
}

// SetOverrunDetect changes the simulated CTRL/STAT.ORUNDETECT bit.
func (t *Target) SetOverrunDetect(enabled bool) {
	if t == nil {
		return
	}
	if enabled {
		t.ctrlStat |= overrunDetect
		return
	}
	t.ctrlStat &^= overrunDetect
}

// OverrunDetectEnabled reports the simulated CTRL/STAT.ORUNDETECT bit.
func (t *Target) OverrunDetectEnabled() bool {
	return t != nil && t.ctrlStat&overrunDetect != 0
}

// ObserveResponse records STICKYORUN after a non-OK acknowledgement while
// overrun detection is active.
func (t *Target) ObserveResponse(err error) {
	if t != nil && err != nil && t.OverrunDetectEnabled() {
		t.ctrlStat |= stickyOverrun
	}
}

// ObserveLineReset applies the simulated DLCR and STICKYORUN effects of a line reset.
func (t *Target) ObserveLineReset() {
	if t == nil {
		return
	}
	t.dpBanks[1] = 0
	if t.OverrunDetectEnabled() {
		t.ctrlStat |= stickyOverrun
	}
}

// Acknowledge returns FAULT for ordinary requests while the simulated debug
// port has sticky state. DPIDR, bank-zero CTRL/STAT, and ABORT remain available
// for recovery.
func (t *Target) Acknowledge(ctx context.Context, req swdsim.Request) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	if req.Addr&3 != 0 || req.Addr > 0x0c {
		return errors.New("dap/sim: invalid request")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sticky := stickyOverrun | stickyCompare | stickyError | writeDataError
	if t.ctrlStat&sticky == 0 || isStickyExempt(req, t.selectDP) {
		return nil
	}
	return swd.ErrFault
}

func isStickyExempt(req swdsim.Request, selectDP uint32) bool {
	if req.AP {
		return false
	}
	if !req.Read {
		return req.Addr == uint8(0x00)
	}
	return req.Addr == uint8(0x00) || req.Addr == uint8(0x04) && selectDP&0x0f == 0
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

// AddMEMAP adds a memory access port initialized from aligned words. It rejects
// a non-MEM-AP identity, an existing selector, and unaligned fixtures.
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
	memory := make(map[uint64]byte, len(words)*4)
	for addr, value := range words {
		if addr&3 != 0 {
			return fmt.Errorf("dap/sim: unaligned target-word address %#08x", addr)
		}
		for offset := range 4 {
			memory[uint64(addr)+uint64(offset)] = byte(value >> uint(offset*8))
		}
	}
	t.aps[sel] = &accessPort{
		regs:   map[uint8]uint32{0xf4: 0, 0xfc: idr},
		memory: memory,
		memAP:  true,
		sizes:  1<<0 | 1<<1 | 1<<2,
	}
	return nil
}

// SetMEMAPCFG changes one simulated MEM-AP's CFG register.
func (t *Target) SetMEMAPCFG(sel dap.APSel, cfg uint32) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	selection, err := sel.Value()
	if err != nil {
		return err
	}
	ap := t.aps[sel]
	if ap == nil || !ap.memAP {
		return fmt.Errorf("dap/sim: AP %d is not a MEM-AP", selection)
	}
	ap.regs[0xf4] = cfg
	if cfg&(1<<1) == 0 {
		ap.regs[8] = 0
	}
	if cfg&(1<<2) != 0 {
		ap.sizes |= 1 << 3
	} else {
		ap.sizes &^= 1 << 3
	}
	ap.largePhase = largeDataIdle
	return nil
}

// SetMEMAPSizes changes the transfer sizes accepted by one simulated MEM-AP.
// Size32 is always retained, as required by ADIv5.
func (t *Target) SetMEMAPSizes(sel dap.APSel, sizes ...dap.TransferSize) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	selection, err := sel.Value()
	if err != nil {
		return err
	}
	ap := t.aps[sel]
	if ap == nil || !ap.memAP {
		return fmt.Errorf("dap/sim: AP %d is not a MEM-AP", selection)
	}
	accepted := uint8(1 << 2)
	for _, size := range sizes {
		encoding, ok := transferSizeEncoding(size)
		if !ok {
			return fmt.Errorf("dap/sim: invalid MEM-AP transfer size %d", size)
		}
		if size == dap.Size64 && ap.regs[0xf4]&(1<<2) == 0 {
			return errors.New("dap/sim: Size64 requires CFG.LD")
		}
		accepted |= 1 << encoding
	}
	ap.sizes = accepted
	ap.regs[0] = ap.regs[0]&^uint32(7) | 2
	ap.largePhase = largeDataIdle
	return nil
}

// SetMEMAPBytes copies data into one simulated MEM-AP's target memory.
func (t *Target) SetMEMAPBytes(sel dap.APSel, addr uint64, data []byte) error {
	if t == nil {
		return errors.New("dap/sim: nil target")
	}
	selection, err := sel.Value()
	if err != nil {
		return err
	}
	ap := t.aps[sel]
	if ap == nil || !ap.memAP {
		return fmt.Errorf("dap/sim: AP %d is not a MEM-AP", selection)
	}
	if _, err := memoryRangeEnd(addr, len(data)); err != nil {
		return err
	}
	for i := range data {
		ap.memory[addr+uint64(i)] = data[i]
	}
	return nil
}

// MEMAPBytes returns a copy of one simulated MEM-AP's target memory range.
func (t *Target) MEMAPBytes(sel dap.APSel, addr uint64, size int) ([]byte, error) {
	if t == nil {
		return nil, errors.New("dap/sim: nil target")
	}
	selection, err := sel.Value()
	if err != nil {
		return nil, err
	}
	ap := t.aps[sel]
	if ap == nil || !ap.memAP {
		return nil, fmt.Errorf("dap/sim: AP %d is not a MEM-AP", selection)
	}
	if _, err := memoryRangeEnd(addr, size); err != nil {
		return nil, err
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = ap.memory[addr+uint64(i)]
	}
	return data, nil
}

func memoryRangeEnd(addr uint64, size int) (uint64, error) {
	if size < 0 {
		return 0, errors.New("dap/sim: negative memory range size")
	}
	if size == 0 {
		return addr, nil
	}
	end := addr + uint64(size-1)
	if end < addr {
		return 0, errors.New("dap/sim: memory range overflows")
	}
	return end, nil
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
	value, err := ap.readRegister(reg)
	if err != nil {
		return 0, err
	}
	t.rdbuff = value
	return posted, nil
}

func (t *Target) writeAP(req swdsim.Request, value uint32) error {
	ap := t.aps[dap.NewAPSel(uint8(t.selectDP>>24))]
	if ap == nil {
		return nil
	}
	return ap.writeRegister(t.apReg(req), value)
}

func (ap *accessPort) readRegister(reg uint8) (uint32, error) {
	if !ap.memAP {
		return ap.regs[reg], nil
	}
	if err := ap.validateLargeDataRegister(reg, "read"); err != nil {
		return 0, err
	}
	if reg == 8 && ap.regs[0xf4]&(1<<1) == 0 {
		return 0, nil
	}
	if reg == 0x0c {
		return ap.readDRW()
	}
	if reg == 0 {
		ap.largePhase = largeDataIdle
	}
	return ap.regs[reg], nil
}

func (ap *accessPort) writeRegister(reg uint8, value uint32) error {
	if !ap.memAP {
		ap.regs[reg] = value
		return nil
	}
	if err := ap.validateLargeDataRegister(reg, "write"); err != nil {
		return err
	}
	if reg == 8 && ap.regs[0xf4]&(1<<1) == 0 {
		return nil
	}
	if reg == 0x0c {
		return ap.writeDRW(value)
	}
	if reg == 0 && ap.sizes&(1<<uint8(value&7)) == 0 {
		value = value&^uint32(7) | ap.regs[0]&7
	}
	ap.regs[reg] = value
	if reg == 0 {
		ap.largePhase = largeDataIdle
	}
	return nil
}

func (ap *accessPort) validateLargeDataRegister(reg uint8, operation string) error {
	if ap.largePhase == largeDataIdle || reg == 0 || reg == 0x0c {
		return nil
	}
	return fmt.Errorf("dap/sim: MEM-AP register %#02x %s during incomplete 64-bit transfer", reg, operation)
}

func (ap *accessPort) readDRW() (uint32, error) {
	size := uint8(ap.regs[0] & 7)
	if size > 3 || ap.regs[0]&0x30 != 0 {
		return 0, errors.New("dap/sim: DRW read has unsupported CSW.Size or AddrInc")
	}
	if size == 3 {
		if ap.regs[0xf4]&(1<<2) == 0 {
			return 0, errors.New("dap/sim: 64-bit DRW read requires CFG.LD")
		}
		switch ap.largePhase {
		case largeDataIdle:
			value := ap.memoryValue(ap.targetAddress(), 8)
			ap.largeWord = uint32(value >> 32)
			ap.largePhase = largeDataRead
			return uint32(value), nil
		case largeDataRead:
			value := ap.largeWord
			ap.largePhase = largeDataIdle
			return value, nil
		default:
			return 0, errors.New("dap/sim: 64-bit DRW read interrupted a write")
		}
	}
	width := 1 << size
	value := uint32(ap.memoryValue(ap.targetAddress(), width))
	return value << ap.laneShift(width), nil
}

func (ap *accessPort) writeDRW(value uint32) error {
	size := uint8(ap.regs[0] & 7)
	if size > 3 || ap.regs[0]&0x30 != 0 {
		return errors.New("dap/sim: DRW write has unsupported CSW.Size or AddrInc")
	}
	if size == 3 {
		if ap.regs[0xf4]&(1<<2) == 0 {
			return errors.New("dap/sim: 64-bit DRW write requires CFG.LD")
		}
		switch ap.largePhase {
		case largeDataIdle:
			ap.largeWord = value
			ap.largePhase = largeDataWrite
			return nil
		case largeDataWrite:
			ap.writeMemoryValue(ap.targetAddress(), 8, uint64(ap.largeWord)|uint64(value)<<32)
			ap.largePhase = largeDataIdle
			return nil
		default:
			return errors.New("dap/sim: 64-bit DRW write interrupted a read")
		}
	}
	width := 1 << size
	data := uint64(value >> ap.laneShift(width))
	ap.writeMemoryValue(ap.targetAddress(), width, data)
	return nil
}

func (ap *accessPort) targetAddress() uint64 {
	address := uint64(ap.regs[4])
	if ap.regs[0xf4]&(1<<1) != 0 {
		address |= uint64(ap.regs[8]) << 32
	}
	return address
}

func (ap *accessPort) laneShift(width int) uint {
	lane := int(ap.targetAddress() & 3)
	if ap.regs[0xf4]&1 != 0 {
		lane = 4 - width - lane
	}
	return uint(lane * 8)
}

func (ap *accessPort) memoryValue(addr uint64, width int) uint64 {
	var value uint64
	if ap.regs[0xf4]&1 != 0 {
		for offset := range width {
			value = value<<8 | uint64(ap.memory[addr+uint64(offset)])
		}
		return value
	}
	for offset := range width {
		value |= uint64(ap.memory[addr+uint64(offset)]) << uint(offset*8)
	}
	return value
}

func (ap *accessPort) writeMemoryValue(addr uint64, width int, value uint64) {
	for offset := range width {
		shift := offset * 8
		if ap.regs[0xf4]&1 != 0 {
			shift = (width - 1 - offset) * 8
		}
		ap.memory[addr+uint64(offset)] = byte(value >> uint(shift))
	}
}

func (t *Target) apReg(req swdsim.Request) uint8 {
	bank := uint8(t.selectDP>>4) & 0x0f
	return bank<<4 | req.Addr
}

func transferSizeEncoding(size dap.TransferSize) (uint8, bool) {
	switch size {
	case dap.Size8:
		return 0, true
	case dap.Size16:
		return 1, true
	case dap.Size32:
		return 2, true
	case dap.Size64:
		return 3, true
	default:
		return 0, false
	}
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
	control := value & (overrunDetect | debugPowerRequest | systemPowerRequest)
	requests := control & (debugPowerRequest | systemPowerRequest)
	t.ctrlStat = sticky | control
	if requests&debugPowerRequest != 0 {
		t.ctrlStat |= debugPowerAck
	}
	if requests&systemPowerRequest != 0 {
		t.ctrlStat |= systemPowerAck
	}
}
