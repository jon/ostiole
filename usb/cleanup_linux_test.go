//go:build linux

package usb

import (
	"errors"
	"os"
	"testing"
)

func TestLinuxCleanupStateFollowsFinalFileClose(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "usb")
	if err != nil {
		t.Fatal(err)
	}
	d := &Device{file: file}
	if !d.CleanupPending() {
		t.Fatal("open file not owned")
	}
	if err := d.Close(); err != nil || d.CleanupPending() {
		t.Fatalf("Close() = %v, pending=%t", err, d.CleanupPending())
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("file remained open")
	}
}
