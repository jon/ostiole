//go:build darwin && cgo

package usb

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIOKitNativeLifecycle(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "iokit-lifecycle")
	build := exec.Command("clang", "-std=c11", "-Wall", "-Wextra", "-Wpedantic", "-Werror",
		"testdata/iokit_lifecycle.c", "iokit_darwin.c", "-framework", "CoreFoundation", "-framework", "IOKit", "-o", binary)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile native lifecycle fixture: %v\n%s", err, output)
	}
	for _, scenario := range []string{"live-close", "missing-source", "no-device", "not-attached", "invalid-destination"} {
		t.Run(scenario, func(t *testing.T) {
			if output, err := exec.Command(binary, scenario).CombinedOutput(); err != nil {
				t.Fatalf("native lifecycle: %v\n%s", err, output)
			}
		})
	}
}
