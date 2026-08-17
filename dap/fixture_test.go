package dap_test

import "github.com/jon/ostiole/dap"

func apSel(value uint8) dap.APSel {
	return dap.NewAPSel(value)
}
