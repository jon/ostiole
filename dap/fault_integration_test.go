//go:build integration

package dap_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
)

func TestRecoverTargetGeneratedFAULTOverFTDI(t *testing.T) {
	if os.Getenv("OSTIOLE_FTDI_HIL_FAULT") != "1" {
		t.Skip("OSTIOLE_FTDI_HIL_FAULT is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dp, wire := openHardwareDebugPortWithFaultWire(t, ctx)
	t.Cleanup(func() { releaseHardwareDebugPort(t, dp) })

	if _, err := dp.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := dp.ReadDP(ctx, dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	if state&1 == 0 {
		t.Fatal("SWD connection did not enable ORUNDETECT")
	}
	wire.armed = true
	if err := dp.WriteDP(ctx, dap.SELECT, 0); err != nil {
		t.Fatal(err)
	}
	_, err = dp.ReadAPIDR(ctx, hardwareAP)
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !errors.Is(err, swd.ErrFault) {
		t.Fatalf("ReadAPIDR() error after corrupted write parity = %v, want typed FAULT", err)
	}
	if !fault.StateValid || fault.CTRLSTAT&(1<<7) == 0 {
		t.Fatalf("FaultError = %+v, want captured WDATAERR", fault)
	}
	t.Logf("target-generated FAULT: CTRL/STAT=%#08x", fault.CTRLSTAT)
	if _, err := dp.ReadAPIDR(ctx, hardwareAP); err != nil {
		t.Fatalf("AP read after clearing WDATAERR: %v", err)
	}
	state, err = dp.ReadDP(ctx, dap.CTRLSTAT)
	if err != nil {
		t.Fatal(err)
	}
	if state&(1<<7|1<<1) != 0 {
		t.Fatalf("CTRL/STAT = %#08x after FAULT recovery", state)
	}
	if counter := wire.inner.(*ackCountingWire); counter.fixed == 0 {
		t.Fatal("fault experiment did not clock fixed overrun-response frames")
	}
}
