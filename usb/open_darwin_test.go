//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDarwinHost struct {
	fakeDarwinInventory
	device   darwinDeviceHandle
	openErr  error
	openedAt []uint32
}

func (f *fakeDarwinHost) open(location uint32) (darwinDeviceHandle, error) {
	f.openedAt = append(f.openedAt, location)
	return f.device, f.openErr
}

type fakeDarwinDevice struct {
	attachment  darwinAttachment
	identityErr error
	closeErr    error
	closes      int
}

func (f *fakeDarwinDevice) identity() (darwinAttachment, error) {
	return f.attachment, f.identityErr
}

func (f *fakeDarwinDevice) close() error {
	f.closes++
	return f.closeErr
}

func TestDarwinOpenRevalidatesBeforeAndAfterOpening(t *testing.T) {
	attachment := darwinAttachment{
		vid:      0x0403,
		pid:      0x6014,
		location: 0x02123456,
		address:  9,
	}
	native := &fakeDarwinDevice{attachment: attachment}
	host := &fakeDarwinHost{
		fakeDarwinInventory: fakeDarwinInventory{
			attachments: []darwinAttachment{attachment},
		},
		device: native,
	}
	bus := newDarwinEnumerator(host)
	device, err := bus.Open(context.Background(), attachment.info())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(host.openedAt) != 1 || host.openedAt[0] != attachment.location {
		t.Fatalf("opened locations = %#v", host.openedAt)
	}
	if got, want := device.Identity(), attachment.info(); got != want {
		t.Fatalf("Identity() = %+v, want %+v", got, want)
	}
	if err := device.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if device.handle != nil {
		t.Fatal("Close retained the native device handle")
	}
	if err := device.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if native.closes != 1 {
		t.Fatalf("native closes = %d, want 1", native.closes)
	}
}

func TestDarwinOpenRejectsStaleCandidates(t *testing.T) {
	want := DeviceInfo{VID: 0x0403, PID: 0x6014, Bus: 2, Address: 9}
	tests := []struct {
		name        string
		attachments []darwinAttachment
		postOpen    darwinAttachment
		wantClose   int
	}{
		{name: "disappeared"},
		{
			name: "identity changed before open",
			attachments: []darwinAttachment{{
				vid:      0x0403,
				pid:      0x6001,
				location: 0x02000000,
				address:  9,
			}},
		},
		{
			name: "identity changed after open",
			attachments: []darwinAttachment{{
				vid:      0x0403,
				pid:      0x6014,
				location: 0x02000000,
				address:  9,
			}},
			postOpen: darwinAttachment{
				vid:      0x0403,
				pid:      0x6001,
				location: 0x02000000,
				address:  9,
			},
			wantClose: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			native := &fakeDarwinDevice{attachment: test.postOpen}
			host := &fakeDarwinHost{
				fakeDarwinInventory: fakeDarwinInventory{
					attachments: test.attachments,
				},
				device: native,
			}
			bus := newDarwinEnumerator(host)
			_, err := bus.Open(context.Background(), want)
			if !errors.Is(err, ErrStaleCandidate) {
				t.Fatalf("Open error = %v", err)
			}
			if native.closes != test.wantClose {
				t.Fatalf("native closes = %d, want %d", native.closes, test.wantClose)
			}
		})
	}
}

func TestDarwinOpenRejectsChangedUSBSerial(t *testing.T) {
	expected := DeviceInfo{VID: 0x1366, PID: 0x1020, Bus: 2, Address: 1, Serial: "old"}
	actual := DeviceInfo{VID: 0x1366, PID: 0x1020, Bus: 2, Address: 1, Serial: "new"}
	if err := validateDarwinIdentity(expected, actual); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("validateDarwinIdentity() error = %v, want ErrStaleCandidate", err)
	}
}

func TestDarwinOpenValidatesContextAndCleansUpIdentityFailure(t *testing.T) {
	wantIdentity := errors.New("identity failed")
	native := &fakeDarwinDevice{identityErr: wantIdentity}
	host := &fakeDarwinHost{device: native}
	bus := newDarwinEnumerator(host)

	var nilContext context.Context
	if _, err := bus.Open(nilContext, DeviceInfo{}); err == nil {
		t.Fatal("Open(nil) succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bus.Open(ctx, DeviceInfo{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open(canceled) error = %v", err)
	}
	var zeroBus Enumerator
	if _, err := zeroBus.Open(context.Background(), DeviceInfo{}); err == nil {
		t.Fatal("zero Enumerator.Open succeeded")
	}

	attachment := darwinAttachment{vid: 1, pid: 2, location: 3, address: 4}
	host.attachments = []darwinAttachment{attachment}
	_, err := bus.Open(context.Background(), attachment.info())
	if !errors.Is(err, wantIdentity) {
		t.Fatalf("Open error = %v, want %v", err, wantIdentity)
	}
	if native.closes != 1 {
		t.Fatalf("native closes = %d, want 1", native.closes)
	}
}

func TestDarwinOpenReportsNativeOpenAndCloseFailures(t *testing.T) {
	attachment := darwinAttachment{vid: 1, pid: 2, location: 3, address: 4}
	wantOpen := errors.New("open failed")
	host := &fakeDarwinHost{
		fakeDarwinInventory: fakeDarwinInventory{
			attachments: []darwinAttachment{attachment},
		},
		openErr: wantOpen,
	}
	bus := newDarwinEnumerator(host)
	if _, err := bus.Open(context.Background(), attachment.info()); !errors.Is(err, wantOpen) {
		t.Fatalf("Open error = %v, want %v", err, wantOpen)
	}

	wantClose := errors.New("close failed")
	native := &fakeDarwinDevice{attachment: attachment, closeErr: wantClose}
	host.openErr, host.device = nil, native
	device, err := bus.Open(context.Background(), attachment.info())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := device.Close(); !errors.Is(err, wantClose) {
		t.Fatalf("Close error = %v, want %v", err, wantClose)
	}
	if err := device.Close(); !errors.Is(err, wantClose) {
		t.Fatalf("second Close error = %v, want %v", err, wantClose)
	}
	if native.closes != 1 {
		t.Fatalf("native closes = %d, want 1", native.closes)
	}
}

func TestDarwinDeviceCloseJoinsNativeCleanupResults(t *testing.T) {
	if err := joinIOKitCleanupCodes(0, 0); err != nil {
		t.Fatalf("successful cleanup error = %v", err)
	}
	err := joinIOKitCleanupCodes(0xe00002c0, 0xe00002c2)
	if err == nil ||
		!strings.Contains(err.Error(), "0xe00002c0") ||
		!strings.Contains(err.Error(), "0xe00002c2") {
		t.Fatalf("joined cleanup error = %v", err)
	}
}
