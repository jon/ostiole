//go:build integration

package dap_test

import (
	"context"
	"fmt"
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

func TestWriteMEMAPBlocksOverFTDI(t *testing.T) {
	if os.Getenv("OSTIOLE_FTDI_HIL_WRITE") != "1" {
		t.Skip("OSTIOLE_FTDI_HIL_WRITE is not 1")
	}
	addr, err := strconv.ParseUint(os.Getenv("OSTIOLE_FTDI_HIL_SCRATCH"), 0, 64)
	if err != nil {
		t.Fatalf("parse OSTIOLE_FTDI_HIL_SCRATCH: %v", err)
	}
	if addr%hardwareScratchSize != 0 {
		t.Fatalf("OSTIOLE_FTDI_HIL_SCRATCH = %#x, want a 64-byte-aligned address", addr)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dp := openHardwareDebugPort(t, ctx)
	var (
		mem           *dap.MemAP
		original      []byte
		restored      bool
		expectedDPIDR uint32
		expectedAPIDR uint32
	)
	t.Cleanup(func() {
		if mem != nil && len(original) != 0 && !restored {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cleanupCancel()
			if err := restoreHardwareScratch(cleanupCtx, dp, &mem, addr, original, expectedDPIDR, expectedAPIDR); err != nil {
				t.Errorf("restore scratch memory within cleanup deadline: %v", err)
			}
		}
		if mem != nil {
			releaseHardwareMemAP(t, mem)
		}
		releaseHardwareDebugPort(t, dp)
	})
	identity, err := dp.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedDPIDR = identity.Raw
	apIdentity, err := dp.ReadAPIDR(ctx, hardwareAP)
	if err != nil {
		t.Fatal(err)
	}
	expectedAPIDR = apIdentity.Raw
	mem, err = dap.OpenMemAP(ctx, dp, hardwareAP)
	if err != nil {
		t.Fatal(err)
	}
	original, err = readHardwareBytes(ctx, mem, addr, hardwareScratchSize)
	if err != nil {
		t.Fatal(err)
	}

	aligned := make([]byte, hardwareScratchSize)
	for i := range aligned {
		aligned[i] = byte(i*29 + 3)
	}
	if err := writeAndVerifyHardwareBlock(ctx, mem, addr, aligned, aligned); err != nil {
		t.Fatal(err)
	}
	if err := writeAndVerifyHardwareBlock(ctx, mem, addr, original, original); err != nil {
		t.Fatal(err)
	}

	unaligned := make([]byte, 31)
	for i := range unaligned {
		unaligned[i] = byte(i*41 + 5)
	}
	want := slices.Clone(original)
	copy(want[1:], unaligned)
	n, err := mem.WriteBlock(ctx, addr+1, unaligned)
	if err != nil || n != len(unaligned) {
		t.Fatalf("unaligned WriteBlock() = %d, %v, want %d, nil", n, err, len(unaligned))
	}
	if err := assertHardwareBlock(ctx, mem, addr, want); err != nil {
		t.Fatal(err)
	}
	if err := writeAndVerifyHardwareBlock(ctx, mem, addr, original, original); err != nil {
		t.Fatal(err)
	}
	restored = true
	t.Logf("block writes: scratch=%#x aligned=%d unaligned=%d neighbors=unchanged restored=true", addr, len(aligned), len(unaligned))
}

func writeAndVerifyHardwareBlock(ctx context.Context, mem *dap.MemAP, addr uint64, data, want []byte) error {
	n, err := mem.WriteBlock(ctx, addr, data)
	if err != nil || n != len(data) {
		return fmt.Errorf("WriteBlock() = %d, %v, want %d, nil", n, err, len(data))
	}
	return assertHardwareBlock(ctx, mem, addr, want)
}

func assertHardwareBlock(ctx context.Context, mem *dap.MemAP, addr uint64, want []byte) error {
	got := make([]byte, len(want))
	n, err := mem.ReadBlock(ctx, addr, got)
	if err != nil || n != len(got) {
		return fmt.Errorf("ReadBlock() = %d, %v, want %d, nil", n, err, len(got))
	}
	if !slices.Equal(got, want) {
		return fmt.Errorf("scratch bytes = % x, want % x", got, want)
	}
	return nil
}
