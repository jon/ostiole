//go:build integration

package dap_test

import (
	"context"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
)

const cortexMCPUID = uint32(0xe000ed00)

func TestReadMEMAPWordOverFTDI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dp := openHardwareDebugPort(t, ctx)
	var (
		savedCSW uint32
		savedTAR uint32
		saved    bool
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		defer cleanupCancel()
		if saved {
			if err := dp.WriteAP(cleanupCtx, hardwareAP, dap.APTAR, savedTAR); err != nil {
				t.Errorf("restore AP0 TAR: %v", err)
			}
			if err := dp.WriteAP(cleanupCtx, hardwareAP, dap.APCSW, savedCSW); err != nil {
				t.Errorf("restore AP0 CSW: %v", err)
			}
			if err := dp.WriteDP(cleanupCtx, dap.SELECT, 0); err != nil {
				t.Errorf("restore DP SELECT: %v", err)
			}
		}
		if err := dp.Release(cleanupCtx); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	info, err := dp.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	savedCSW, err = dp.ReadAP(ctx, hardwareAP, dap.APCSW)
	if err != nil {
		t.Fatal(err)
	}
	savedTAR, err = dp.ReadAP(ctx, hardwareAP, dap.APTAR)
	if err != nil {
		t.Fatal(err)
	}
	saved = true

	mem, err := dap.NewMemAP(ctx, dp, hardwareAP)
	if err != nil {
		t.Fatal(err)
	}
	cpuid, err := mem.ReadWord(ctx, cortexMCPUID)
	if err != nil {
		t.Fatal(err)
	}
	if cpuid>>24 != 0x41 || cpuid>>4&0x0fff == 0 {
		t.Fatalf("CPUID = %#08x, want a plausible Cortex-M identity", cpuid)
	}
	t.Logf("DPIDR=%#08x CPUID=%#08x", info.Raw, cpuid)
}
