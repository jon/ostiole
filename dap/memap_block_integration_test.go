//go:build integration

package dap_test

import (
	"context"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
)

func TestReadMEMAPBlockOverFTDI(t *testing.T) {
	rawAddr := os.Getenv("OSTIOLE_FTDI_HIL_SCRATCH")
	if rawAddr == "" {
		t.Skip("OSTIOLE_FTDI_HIL_SCRATCH is not set")
	}
	addr, err := strconv.ParseUint(rawAddr, 0, 64)
	if err != nil {
		t.Fatalf("parse OSTIOLE_FTDI_HIL_SCRATCH: %v", err)
	}
	if addr%hardwareScratchSize != 0 {
		t.Fatalf("OSTIOLE_FTDI_HIL_SCRATCH = %#x, want a 64-byte-aligned address", addr)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dp := openHardwareDebugPort(t, ctx)
	var mem *dap.MemAP
	t.Cleanup(func() {
		if mem != nil {
			releaseHardwareMemAP(t, mem)
		}
		releaseHardwareDebugPort(t, dp)
	})
	if _, err := dp.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	mem, err = dap.OpenMemAP(ctx, dp, hardwareAP)
	if err != nil {
		t.Fatal(err)
	}
	want, err := readHardwareBytes(ctx, mem, addr, hardwareScratchSize)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, hardwareScratchSize)
	n, err := mem.ReadBlock(ctx, addr, got)
	if err != nil || n != len(got) {
		t.Fatalf("ReadBlock() = %d, %v; want %d, nil", n, err, len(got))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("block bytes = % x, scalar bytes = % x", got, want)
	}
	t.Logf("block read: scratch=%#x bytes=%d scalar_match=true TAR_boundary=false", addr, len(got))
}
