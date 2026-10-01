package cortexm_test

import (
	"os/exec"
	"testing"
)

func TestRP2350PreparationGuards(t *testing.T) {
	path, err := exec.LookPath("tclsh")
	if err != nil {
		t.Skip("tclsh is not installed")
	}
	cmd := exec.CommandContext(t.Context(), path, "testdata/rp2350-dual-counter/prepare_test.tcl")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("preparation guard regression: %v\n%s", err, output)
	}
}
