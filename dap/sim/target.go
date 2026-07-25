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
}

// New returns an SW-DP target with the supplied identity.
func New(dpidr uint32) *Target {
	return &Target{dpidr: dpidr}
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
		return 0, errors.New("dap/sim: no access port is modeled")
	}
	switch req.Addr {
	case 0x00:
		return t.dpidr, nil
	case 0x04:
		return t.ctrlStat, nil
	case 0x0c:
		return 0, nil
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
		return errors.New("dap/sim: no access port is modeled")
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
