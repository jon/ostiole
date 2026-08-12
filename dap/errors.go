package dap

import (
	"strings"

	"github.com/jon/ostiole/swd"
)

// FaultError reports the CTRL/STAT sticky state captured after an SWD FAULT.
// StateValid is false when DAP could not safely read bank-zero CTRL/STAT.
type FaultError struct {
	CTRLSTAT   uint32
	StateValid bool
}

func (e *FaultError) Error() string {
	if e == nil || !e.StateValid {
		return "dap: SWD FAULT (CTRL/STAT unavailable)"
	}
	var set []string
	if e.CTRLSTAT&stickyCompare != 0 {
		set = append(set, "STICKYCMP")
	}
	if e.CTRLSTAT&stickyError != 0 {
		set = append(set, "STICKYERR")
	}
	if e.CTRLSTAT&writeDataError != 0 {
		set = append(set, "WDATAERR")
	}
	if e.CTRLSTAT&stickyOverrun != 0 {
		set = append(set, "STICKYORUN")
	}
	if len(set) == 0 {
		return "dap: SWD FAULT (CTRL/STAT sticky bits clear)"
	}
	return "dap: SWD FAULT (" + strings.Join(set, ", ") + ")"
}

// Unwrap returns swd.ErrFault.
func (e *FaultError) Unwrap() error {
	return swd.ErrFault
}
