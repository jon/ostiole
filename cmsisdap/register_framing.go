package cmsisdap

import (
	"errors"

	"github.com/jon/ostiole/dap"
)

func (r *RegisterSWD) validateFramingWrite(addr, flags byte, value uint32) error {
	if addr != 4 || flags != 0 {
		return nil
	}
	if !r.bankKnown {
		return errors.New("cmsisdap: DP bank is unknown before write at offset 4")
	}
	if r.bank == 0 && value&1 != 0 {
		return errors.New("cmsisdap: CTRL/STAT.ORUNDETECT is owned by the register transport")
	}
	if r.bank == 1 && value&(3<<8) != 0 {
		return errors.New("cmsisdap: DLCR turnaround changes are not supported")
	}
	return nil
}

func (r *RegisterSWD) observeBank(addr, flags byte, value uint32, status dap.RegisterStatus) {
	if addr != 8 || flags != 0 {
		return
	}
	switch status {
	case dap.RegisterCompleted:
		r.bank, r.bankKnown = uint8(value&15), true
	case dap.RegisterIndeterminate:
		r.bankKnown = false
	}
}
