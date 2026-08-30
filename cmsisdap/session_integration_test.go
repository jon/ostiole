//go:build integration

package cmsisdap_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/cmsisdap"
	"github.com/jon/ostiole/usb"
)

const cmsisdapCloseAttempts = 3

func TestHILCMSISDAPV1IsRejectedBeforeClaim(t *testing.T) {
	if os.Getenv("OSTIOLE_CMSISDAP_V1_HIL") != "1" {
		t.Skip("OSTIOLE_CMSISDAP_V1_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	device := openCMSISDAPUSBDevice(t, ctx)
	defer func() {
		closeCMSISDAPOwner(t, device, "CMSIS-DAP v1 USB device")
	}()

	session, err := cmsisdap.Open(ctx, device)
	if session != nil {
		closeCMSISDAPOwner(t, session, "unexpected CMSIS-DAP session")
		t.Fatalf("cmsisdap.Open() returned a session for CMSIS-DAP v1")
	}
	if !errors.Is(err, cmsisdap.ErrNoV2Interface) {
		t.Fatalf("cmsisdap.Open() error = %v, want ErrNoV2Interface", err)
	}
	identity := device.Identity()
	t.Logf("rejected CMSIS-DAP v1 attachment %04x:%04x product=%q before claiming an interface", identity.VID, identity.PID, identity.Product)
}

func TestHILCMSISDAPV2MetadataSurvivesReopen(t *testing.T) {
	if os.Getenv("OSTIOLE_CMSISDAP_HIL") != "1" {
		t.Skip("OSTIOLE_CMSISDAP_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	first := openCMSISDAPSession(t, ctx)
	firstInfo := first.Info()
	if firstInfo.ProtocolVersion == "" || firstInfo.PacketSize == 0 || firstInfo.PacketCount == 0 {
		t.Fatalf("incomplete CMSIS-DAP metadata = %#v", firstInfo)
	}
	if !closeCMSISDAPOwner(t, first, "first CMSIS-DAP session") {
		return
	}

	second := openCMSISDAPSession(t, ctx)
	defer func() {
		closeCMSISDAPOwner(t, second, "reopened CMSIS-DAP session")
	}()
	secondInfo := second.Info()
	if firstInfo.USB != secondInfo.USB || firstInfo.ProtocolVersion != secondInfo.ProtocolVersion || firstInfo.FirmwareVersion != secondInfo.FirmwareVersion {
		t.Fatalf("metadata changed across reopen: first %#v, second %#v", firstInfo, secondInfo)
	}
	t.Logf("CMSIS-DAP protocol=%q firmware=%q packet_size=%d packet_count=%d capabilities=%x", secondInfo.ProtocolVersion, secondInfo.FirmwareVersion, secondInfo.PacketSize, secondInfo.PacketCount, secondInfo.Capabilities.Bytes())
}

func openCMSISDAPSession(t *testing.T, ctx context.Context) *cmsisdap.Session {
	t.Helper()
	device := openCMSISDAPUSBDevice(t, ctx)
	session, err := cmsisdap.Open(ctx, device)
	if err != nil {
		closeCMSISDAPOwner(t, device, "CMSIS-DAP USB device after failed open")
		t.Fatal(err)
	}
	return session
}

func openCMSISDAPUSBDevice(t *testing.T, ctx context.Context) *usb.Device {
	t.Helper()
	enumerator := usb.New()
	devices, err := enumerator.List(ctx, []usb.DeviceFilter{usb.AllDevices()})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := selectCMSISDAPHILCandidate(devices, os.Getenv("OSTIOLE_CMSISDAP_HIL_DEVICE"), os.Getenv("OSTIOLE_CMSISDAP_HIL_SERIAL"))
	if errors.Is(err, errCMSISDAPHILUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	device, err := enumerator.Open(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func closeCMSISDAPOwner(t *testing.T, owner interface{ Close() error }, description string) bool {
	t.Helper()
	var err error
	for range cmsisdapCloseAttempts {
		if err = owner.Close(); err == nil {
			return true
		}
	}
	t.Errorf("close %s after %d attempts: %v", description, cmsisdapCloseAttempts, err)
	return false
}
