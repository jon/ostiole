//go:build integration

package jlink_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/jlink"
	"github.com/jon/ostiole/usb"
)

func TestHILJLinkMetadataSurvivesReopen(t *testing.T) {
	if os.Getenv("OSTIOLE_JLINK_HIL") != "1" {
		t.Skip("OSTIOLE_JLINK_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	first := openMetadataSession(t, ctx)
	defer func() {
		closeJLinkOwner(t, first, "first J-Link session")
	}()
	firstInfo := first.Info()
	if firstInfo.Firmware == "" || firstInfo.Capabilities.BitLen() < 32 {
		t.Fatalf("incomplete J-Link metadata = %#v", firstInfo)
	}
	if !closeJLinkOwner(t, first, "first J-Link session") {
		return
	}

	second := openMetadataSession(t, ctx)
	defer func() {
		closeJLinkOwner(t, second, "reopened J-Link session")
	}()
	secondInfo := second.Info()
	if firstInfo.USB.VID != secondInfo.USB.VID || firstInfo.USB.PID != secondInfo.USB.PID || firstInfo.Firmware != secondInfo.Firmware {
		t.Fatalf("metadata changed across reopen: first %#v, second %#v", firstInfo, secondInfo)
	}
	if firstInfo.SelectedInterfaceKnown != secondInfo.SelectedInterfaceKnown || firstInfo.SelectedInterfaceKnown && firstInfo.SelectedInterface != secondInfo.SelectedInterface {
		t.Fatalf("selected interface changed across metadata-only reopen: first %d/%t, second %d/%t", firstInfo.SelectedInterface, firstInfo.SelectedInterfaceKnown, secondInfo.SelectedInterface, secondInfo.SelectedInterfaceKnown)
	}
	t.Logf("J-Link firmware=%q hardware=%#08x capabilities=%d workspace=%d/%t interfaces=%#08x selected=%d/%t", secondInfo.Firmware, secondInfo.Hardware.Raw, secondInfo.Capabilities.BitLen(), secondInfo.Workspace, secondInfo.WorkspaceKnown, secondInfo.AvailableInterfaces, secondInfo.SelectedInterface, secondInfo.SelectedInterfaceKnown)
}

func openMetadataSession(t *testing.T, ctx context.Context) *jlink.Session {
	t.Helper()
	enumerator := usb.New()
	candidates, err := enumerator.List(ctx, jlink.SupportedDevices())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Skipf("require exactly one supported J-Link attachment; found %d", len(candidates))
	}
	device, err := enumerator.Open(ctx, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	session, err := jlink.Open(ctx, device)
	if err != nil {
		closeJLinkOwner(t, device, "J-Link USB device after failed open")
		t.Fatal(err)
	}
	return session
}
