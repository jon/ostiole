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
	var mem *dap.MemAP
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if err := mem.Release(cleanupCtx); err != nil {
			t.Errorf("release MEM-AP: %v", err)
		}
		if err := dp.Release(cleanupCtx); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	info, err := dp.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	savedCSW, err := dp.ReadRawAP(ctx, hardwareAP.Address(0x00))
	if err != nil {
		t.Fatal(err)
	}
	savedTAR, err := dp.ReadRawAP(ctx, hardwareAP.Address(0x04))
	if err != nil {
		t.Fatal(err)
	}
	mem, err = dap.NewMemAP(ctx, dp, hardwareAP)
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
	if err := mem.Release(ctx); err != nil {
		t.Fatal(err)
	}
	assertHardwareAPRegister(t, ctx, dp, 0x00, savedCSW)
	assertHardwareAPRegister(t, ctx, dp, 0x04, savedTAR)
	t.Logf("DPIDR=%#08x CPUID=%#08x", info.Raw, cpuid)
}

func assertHardwareAPRegister(t *testing.T, ctx context.Context, dp *dap.DebugPort, address uint8, want uint32) {
	t.Helper()
	got, err := dp.ReadRawAP(ctx, hardwareAP.Address(address))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("AP0 register %#02x = %#08x, want %#08x", address, got, want)
	}
}
