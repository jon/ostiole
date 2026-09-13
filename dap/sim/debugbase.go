package sim

import (
	"errors"
	"fmt"

	"github.com/jon/ostiole/dap"
)

// SetMEMAPDebugBase sets the raw low and high words of a simulated MEM-AP's
// read-only BASE register. It accepts malformed encodings for failure tests.
// The high word reads as zero unless CFG.LA is set. Newly added MEM-APs report
// no debug entry (low word 2); changing CFG does not change the supplied words.
func (t *Target) SetMEMAPDebugBase(sel dap.APSel, low, high uint32) error {
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
	ap.regs[0xf8], ap.regs[0xf0] = low, high
	return nil
}
