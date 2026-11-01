//go:build darwin && cgo

package usb

import (
	"errors"
	"testing"
)

func TestDarwinDeviceGoneClassification(t *testing.T) {
	for _, code := range []uint32{0xe00002c0, 0xe00002d9, 0x10000003, 0xe00002d6, 0xe00002d5} {
		want := code == 0xe00002c0 || code == 0xe00002d9 || code == 0x10000003
		if err := iokitErrorCode(code); errors.Is(err, ErrDeviceGone) != want {
			t.Fatalf("IOKit %#x = %v, gone=%t", code, err, want)
		}
	}
}
