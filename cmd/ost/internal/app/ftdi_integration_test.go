//go:build integration

package app

import (
	"bytes"
	"fmt"
	"testing"
)

func TestHILListFTDIAttachments(t *testing.T) {
	requireHILBench(t)
	var stdout, stderr bytes.Buffer
	if status := Run(t.Context(), []string{"ftdi", "list"}, &stdout, &stderr); status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var bus, address uint8
	var vid, pid uint16
	if _, err := fmt.Fscanf(&stdout, "%03d:%03d %04x:%04x\n",
		&bus, &address, &vid, &pid); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected additional output %q", stdout.String())
	}
	if vid != 0x0403 || pid != 0x6010 && pid != 0x6011 && pid != 0x6014 {
		t.Fatalf("unsupported identity %04x:%04x", vid, pid)
	}
}
