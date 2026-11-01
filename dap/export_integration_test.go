//go:build integration

package dap

// JTAGOwnedStateForTest exposes acquired power and ORUNDETECT ownership
// to the integration bench.
func JTAGOwnedStateForTest(dp *DebugPort) (uint32, bool) {
	return dp.state.ownedPower, dp.jtag.ownedOverrun
}
