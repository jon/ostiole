//go:build integration

package app

import (
	"bytes"
	"fmt"
	"testing"
)

func TestHILReadSWDIdentity(t *testing.T) {
	requireHILBench(t)
	var stdout, stderr bytes.Buffer
	if status := Run(t.Context(), []string{"swd", "dpidr"}, &stdout, &stderr); status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var dpidr uint32
	if _, err := fmt.Fscanf(&stdout, "DPIDR=0x%08x\n", &dpidr); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected additional output %q", stdout.String())
	}
	if dpidr == 0 || dpidr&1 == 0 {
		t.Fatalf("invalid DPIDR %#08x", dpidr)
	}
}
