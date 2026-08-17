//go:build integration

package dap_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
)

const hardwareScratchSize = 64

func TestWriteMEMAPScalarsOverFTDI(t *testing.T) {
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

	expected := slices.Clone(original)
	accesses := []struct {
		offset int
		size   dap.TransferSize
		width  int
		value  uint64
		fill   byte
	}{
		{offset: 1, size: dap.Size8, width: 1, value: 0x5a, fill: 0x5a},
		{offset: 2, size: dap.Size16, width: 2, value: 0xa5a5, fill: 0xa5},
		{offset: 4, size: dap.Size32, width: 4, value: 0x3c3c3c3c, fill: 0x3c},
	}
	for _, access := range accesses {
		if err := mem.WriteScalar(ctx, addr+uint64(access.offset), access.size, access.value); err != nil {
			t.Fatal(err)
		}
		for i := range access.width {
			expected[access.offset+i] = access.fill
		}
		if err := assertHardwareBytes(ctx, mem, addr, expected); err != nil {
			t.Fatal(err)
		}
	}
	size64 := "not advertised"
	if err := mem.WriteScalar(ctx, addr+8, dap.Size64, 0xc3c3c3c3c3c3c3c3); err == nil {
		for i := range 8 {
			expected[8+i] = 0xc3
		}
		if err := assertHardwareBytes(ctx, mem, addr, expected); err != nil {
			t.Fatal(err)
		}
		size64 = "completed"
	} else if strings.Contains(err.Error(), "does not support 64-bit transfers") {
		size64 = "not accepted"
	} else if !strings.Contains(err.Error(), "CFG.LD") {
		t.Fatalf("Size64 write error = %v, want CFG.LD rejection", err)
	}
	if err := writeHardwareBytes(ctx, mem, addr, original); err != nil {
		t.Fatal(err)
	}
	if err := assertHardwareBytes(ctx, mem, addr, original); err != nil {
		t.Fatal(err)
	}
	restored = true
	t.Logf("scalar writes: scratch=%#x bytes=%d sizes=8,16,32 size64=%s neighboring_lanes=unchanged restored=true", addr, hardwareScratchSize, size64)
}

func restoreHardwareScratch(ctx context.Context, dp *dap.DebugPort, mem **dap.MemAP, addr uint64, original []byte, expectedDPIDR, expectedAPIDR uint32) error {
	var attemptErr error
	identityVerified := true
	for {
		if *mem != nil {
			writeErr := writeHardwareBytes(ctx, *mem, addr, original)
			verifyErr := error(nil)
			if writeErr == nil {
				verifyErr = assertHardwareBytes(ctx, *mem, addr, original)
			}
			if writeErr == nil && verifyErr == nil {
				return nil
			}
			attemptErr = errors.Join(attemptErr, writeErr, verifyErr)
			if err := releaseHardwareMemAPWithin(ctx, *mem); err != nil {
				return errors.Join(attemptErr, err)
			}
			*mem = nil
		}
		if !identityVerified {
			identity, err := dp.Connect(ctx)
			if err != nil {
				attemptErr = errors.Join(attemptErr, err)
				if err := releaseHardwareDebugPortWithin(ctx, dp); err != nil {
					return errors.Join(attemptErr, err)
				}
				continue
			}
			if identity.Raw != expectedDPIDR {
				return errors.Join(attemptErr, fmt.Errorf("reconnected DPIDR = %#08x, want %#08x", identity.Raw, expectedDPIDR))
			}
			apIdentity, err := dp.ReadAPIDR(ctx, hardwareAP)
			if err != nil {
				attemptErr = errors.Join(attemptErr, err)
				if err := releaseHardwareDebugPortWithin(ctx, dp); err != nil {
					return errors.Join(attemptErr, err)
				}
				continue
			}
			if apIdentity.Raw != expectedAPIDR {
				return errors.Join(attemptErr, fmt.Errorf("reconnected APIDR = %#08x, want %#08x", apIdentity.Raw, expectedAPIDR))
			}
			identityVerified = true
		}
		reopened, err := dap.OpenMemAP(ctx, dp, hardwareAP)
		if err == nil {
			*mem = reopened
			continue
		}
		attemptErr = errors.Join(attemptErr, err)
		if ctx.Err() != nil {
			return errors.Join(attemptErr, ctx.Err())
		}
		if err := releaseHardwareDebugPortWithin(ctx, dp); err != nil {
			return errors.Join(attemptErr, err)
		}
		identityVerified = false
	}
}

func readHardwareBytes(ctx context.Context, mem *dap.MemAP, addr uint64, size int) ([]byte, error) {
	data := make([]byte, size)
	for i := range data {
		value, err := mem.ReadScalar(ctx, addr+uint64(i), dap.Size8)
		if err != nil {
			return nil, err
		}
		data[i] = byte(value)
	}
	return data, nil
}

func writeHardwareBytes(ctx context.Context, mem *dap.MemAP, addr uint64, data []byte) error {
	for i := range data {
		if err := mem.WriteScalar(ctx, addr+uint64(i), dap.Size8, uint64(data[i])); err != nil {
			return err
		}
	}
	return nil
}

func assertHardwareBytes(ctx context.Context, mem *dap.MemAP, addr uint64, want []byte) error {
	got, err := readHardwareBytes(ctx, mem, addr, len(want))
	if err != nil {
		return err
	}
	if !slices.Equal(got, want) {
		return fmt.Errorf("scratch bytes = % x, want % x", got, want)
	}
	return nil
}
