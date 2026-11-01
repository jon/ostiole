package usb

import "testing"

func TestEmptyUSBStateHasNoCleanup(t *testing.T) {
	for _, device := range []*Device{nil, {}} {
		if device.CleanupPending() {
			t.Fatal("empty device reported pending cleanup")
		}
	}
	for _, claim := range []*ClaimedInterface{nil, {}} {
		if claim.CleanupPending() {
			t.Fatal("empty claim reported pending cleanup")
		}
	}
}

func assertUSBCleanupPending(t *testing.T, device *Device, claim *ClaimedInterface, wantDevice, wantClaim bool) {
	t.Helper()
	if device.CleanupPending() != wantDevice || claim.CleanupPending() != wantClaim {
		t.Fatalf("cleanup pending: device=%t claim=%t, want %t/%t", device.CleanupPending(), claim.CleanupPending(), wantDevice, wantClaim)
	}
}
