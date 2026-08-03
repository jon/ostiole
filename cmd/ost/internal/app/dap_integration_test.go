//go:build integration

package app

import (
	"bytes"
	"fmt"
	"testing"
)

func TestHILInspectDebugPort(t *testing.T) {
	requireHILBench(t)
	var stdout, stderr bytes.Buffer
	if status := Run(t.Context(), []string{"dap", "dp", "id"}, &stdout, &stderr); status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var dpidr uint32
	var revision, part, version, designer uint32
	var minimal bool
	_, err := fmt.Fscanf(&stdout, "DPIDR=0x%08x REVISION=%d PART=0x%x "+
		"MINIMAL=%t VERSION=%d DESIGNER=0x%x\n",
		&dpidr, &revision, &part, &minimal, &version, &designer)
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected additional output %q", stdout.String())
	}
	if dpidr == 0 || dpidr&1 == 0 || version == 0 || designer == 0 {
		t.Fatalf("invalid DPIDR fields from %#08x", dpidr)
	}
}
