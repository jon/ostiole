//go:build integration

package dap_test

import (
	"context"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
)

func TestReadDPIDROverFTDI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dp := openHardwareDebugPort(t, ctx)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if err := dp.Release(cleanupCtx); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})
	info, err := dp.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DPIDR=%#08x version=%d designer=%#03x", info.Raw, info.Version, info.Designer)
}

func TestConnectAndReleaseOverFTDI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dp := openHardwareDebugPort(t, ctx)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if err := dp.Release(cleanupCtx); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	first, err := dp.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := dp.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("DPIDR changed from %+v to %+v", first, second)
	}
	if err := dp.Release(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("DPIDR=%#08x after reconnect", second.Raw)
}

func TestOverrunResponsesOverFTDI(t *testing.T) {
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
	if _, err := dp.ReadAPIDR(ctx, hardwareAP); err != nil {
		t.Fatal(err)
	}
	counter := wire.inner.(*ackCountingWire)
	if counter.fixed == 0 {
		t.Fatal("connected ORUNDETECT did not use fixed response frames")
	}
	t.Logf("automatic ORUNDETECT fixed response frames=%d", counter.fixed)
}
