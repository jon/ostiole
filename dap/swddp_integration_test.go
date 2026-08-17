//go:build integration

package dap_test

import (
	"context"
	"testing"
	"time"
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
