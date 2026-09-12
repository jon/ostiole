package dap_test

import (
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func swdIdentity(t testing.TB, identity dap.Identity) dap.DPIDRInfo {
	t.Helper()
	dpidr, ok := identity.DPIDR()
	if !ok {
		t.Fatal("SW-DP identity has no DPIDR")
	}
	return dpidr
}

func TestIdentityDistinguishesAbsentRegisters(t *testing.T) {
	var zero dap.Identity
	if value, ok := zero.DPIDR(); ok || value != (dap.DPIDRInfo{}) {
		t.Fatalf("zero DPIDR: %+v, %t", value, ok)
	}
	if value, ok := zero.IDCODE(); ok || value != 0 {
		t.Fatalf("zero IDCODE: %#x, %t", value, ok)
	}
	dp := dap.NewDebugPort(swd.New(swdsim.New(sim.New(0x2ba01477))))
	if _, ok := dp.Identity(); ok {
		t.Fatal("identity known before Connect")
	}
	identity, err := dp.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := identity.DPIDR(); !ok || value.Raw != 0x2ba01477 {
		t.Fatalf("DPIDR: %+v, %t", value, ok)
	}
	if value, ok := identity.IDCODE(); ok || value != 0 {
		t.Fatalf("SWD fabricated IDCODE: %#x, %t", value, ok)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if cached, ok := dp.Identity(); !ok || cached != identity {
		t.Fatalf("cached identity: %+v, %t", cached, ok)
	}
}
