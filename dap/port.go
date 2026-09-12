package dap

import (
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/swd"
)

// Port binds a debug port to one protocol connection without traffic.
// Its zero value is invalid. Construct it with SWDP or JTAGDP.
type Port struct {
	swd      *swd.Conn
	chain    *jtag.Chain
	tapIndex int
	jtag     bool
}

// SWDP binds an SW-DP to conn. Connect validates and enters the connection.
func SWDP(conn *swd.Conn) Port { return Port{swd: conn} }

// JTAGDP binds a baseline ADIv5 JTAG-DP to a zero-based, TDO-first TAP index.
// Connect validates the complete chain and requires a four- or eight-bit IR.
// The caller must give the debug port exclusive use of the chain until release.
func JTAGDP(chain *jtag.Chain, tapIndex int) Port {
	return Port{chain: chain, tapIndex: tapIndex, jtag: true}
}

func (dp *DebugPort) bound() bool {
	return dp != nil && (dp.conn != nil || dp.jtag != nil)
}
