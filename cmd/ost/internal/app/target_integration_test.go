//go:build integration

package app

import (
	"bytes"
	"fmt"
	"testing"
)

func TestHILIdentifyCortexMTarget(t *testing.T) {
	requireHILBench(t)
	var stdout, stderr bytes.Buffer
	args := []string{"target", "cortex-m", "id", "--ap", "0"}
	if status := Run(t.Context(), args, &stdout, &stderr); status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var dpidr, apidr, cpuid uint32
	if _, err := fmt.Fscanf(&stdout, "DPIDR=0x%08x AP0_IDR=0x%08x CPUID=0x%08x\n",
		&dpidr, &apidr, &cpuid); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected additional output %q", stdout.String())
	}
	if dpidr == 0 || dpidr&1 == 0 || apidr == 0 ||
		cpuid>>24 != 0x41 || cpuid>>4&0x0fff == 0 {
		t.Fatalf("invalid identity DPIDR=%#08x AP0_IDR=%#08x CPUID=%#08x",
			dpidr, apidr, cpuid)
	}
}
