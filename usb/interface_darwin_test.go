//go:build darwin && cgo

package usb

import (
	"errors"
	"reflect"
	"testing"
)

type fakeDarwinInterfaceDevice struct {
	fakeDarwinDevice
	iface      darwinInterfaceHandle
	findErr    error
	interfaces []uint8
}

func (f *fakeDarwinInterfaceDevice) interfaceHandle(iface uint8) (darwinInterfaceHandle, error) {
	f.interfaces = append(f.interfaces, iface)
	return f.iface, f.findErr
}

type fakeDarwinInterface struct {
	pipesValue []darwinPipe
	openErr    error
	pipesErr   error
	setAltErr  error
	closeErr   error
	alternates []uint8
	seizes     int
	closes     int
}

func (f *fakeDarwinInterface) openSeize() error {
	f.seizes++
	return f.openErr
}

func (f *fakeDarwinInterface) pipes() ([]darwinPipe, error) {
	return f.pipesValue, f.pipesErr
}

func (f *fakeDarwinInterface) setAlternate(alternate uint8) error {
	f.alternates = append(f.alternates, alternate)
	return f.setAltErr
}

func (f *fakeDarwinInterface) close() error {
	f.closes++
	return f.closeErr
}

func TestDarwinClaimSeizesOnlyTheSelectedInterface(t *testing.T) {
	nativeInterface := &fakeDarwinInterface{pipesValue: []darwinPipe{
		{
			endpoint:     0x02,
			ref:          4,
			transferType: darwinBulkPipe,
		},
	}}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}

	claim, err := device.ClaimInterface(0)
	if err != nil {
		t.Fatalf("ClaimInterface: %v", err)
	}
	if !reflect.DeepEqual(native.interfaces, []uint8{0}) {
		t.Fatalf("selected interfaces = %#v", native.interfaces)
	}
	if nativeInterface.seizes != 1 {
		t.Fatalf("interface seizures = %d, want 1", nativeInterface.seizes)
	}
	if got := device.routes[0x02]; got.ref != 4 {
		t.Fatalf("endpoint route = %#v", got)
	}
	if _, err := device.ClaimInterface(1); err == nil {
		t.Fatal("second ClaimInterface succeeded")
	}
	if err := claim.Close(); err != nil {
		t.Fatalf("ClaimedInterface.Close: %v", err)
	}
	if nativeInterface.closes != 1 {
		t.Fatalf("interface closes = %d, want 1", nativeInterface.closes)
	}
}

func TestDarwinClaimRetainsOwnershipAfterFailedRelease(t *testing.T) {
	want := errors.New("release failed")
	nativeInterface := &fakeDarwinInterface{closeErr: want}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}
	claim, err := device.ClaimInterface(0)
	if err != nil {
		t.Fatalf("ClaimInterface: %v", err)
	}
	if err := claim.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() error = %v, want %v", err, want)
	}
	if _, err := device.ClaimInterface(1); err == nil {
		t.Fatal("ClaimInterface() succeeded while release was pending")
	}
	nativeInterface.closeErr = nil
	if err := claim.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if _, err := device.ClaimInterface(1); err != nil {
		t.Fatalf("ClaimInterface() after release: %v", err)
	}
}

func TestDarwinClaimCleansUpAfterPipeDiscoveryFailure(t *testing.T) {
	want := errors.New("pipe discovery failed")
	nativeInterface := &fakeDarwinInterface{pipesErr: want}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}

	if _, err := device.ClaimInterface(0); !errors.Is(err, want) {
		t.Fatalf("ClaimInterface error = %v, want %v", err, want)
	}
	if nativeInterface.closes != 1 {
		t.Fatalf("interface closes = %d, want 1", nativeInterface.closes)
	}
	if device.claim != nil {
		t.Fatal("failed claim remained active")
	}
}

func TestDarwinClaimCleansUpAfterSeizureFailure(t *testing.T) {
	want := errors.New("seizure failed")
	nativeInterface := &fakeDarwinInterface{openErr: want}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}

	if _, err := device.ClaimInterface(0); !errors.Is(err, want) {
		t.Fatalf("ClaimInterface error = %v, want %v", err, want)
	}
	if nativeInterface.closes != 1 {
		t.Fatalf("interface closes = %d, want 1", nativeInterface.closes)
	}
	if device.claim != nil {
		t.Fatal("failed claim remained active")
	}
}

func TestDarwinClaimCleanupRemainsRetryable(t *testing.T) {
	wantAcquire := errors.New("acquisition failed")
	wantRelease := errors.New("release failed")
	tests := []struct {
		name     string
		openErr  error
		pipesErr error
	}{
		{name: "seizure", openErr: wantAcquire},
		{name: "pipe discovery", pipesErr: wantAcquire},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nativeInterface := &fakeDarwinInterface{openErr: test.openErr, pipesErr: test.pipesErr, closeErr: wantRelease}
			native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
			device := &Device{handle: native}

			claim, err := device.ClaimInterface(0)
			if claim != nil || !errors.Is(err, wantAcquire) || !errors.Is(err, wantRelease) {
				t.Fatalf("ClaimInterface() = (%T, %v), want nil and both errors", claim, err)
			}
			if device.claim == nil {
				t.Fatal("failed cleanup discarded the interface claim")
			}
			nativeInterface.closeErr = nil
			if err := device.Close(); err != nil {
				t.Fatalf("Device.Close() retry: %v", err)
			}
			if nativeInterface.closes != 2 || native.closes != 1 {
				t.Fatalf("close counts = interface %d, device %d", nativeInterface.closes, native.closes)
			}
		})
	}
}

func TestDarwinDeviceCloseWaitsForInterfaceRelease(t *testing.T) {
	wantRelease := errors.New("release failed")
	wantClose := errors.New("device close failed")
	nativeInterface := &fakeDarwinInterface{closeErr: wantRelease}
	native := &fakeDarwinInterfaceDevice{
		fakeDarwinDevice: fakeDarwinDevice{closeErr: wantClose},
		iface:            nativeInterface,
	}
	device := &Device{handle: native}
	if _, err := device.ClaimInterface(0); err != nil {
		t.Fatalf("ClaimInterface: %v", err)
	}
	err := device.Close()
	if !errors.Is(err, wantRelease) {
		t.Fatalf("first Close() error = %v, want %v", err, wantRelease)
	}
	if native.closes != 0 {
		t.Fatalf("device closes after failed release = %d, want 0", native.closes)
	}
	nativeInterface.closeErr = nil
	if err := device.Close(); !errors.Is(err, wantClose) {
		t.Fatalf("second Close() error = %v, want %v", err, wantClose)
	}
	if nativeInterface.closes != 2 || native.closes != 1 {
		t.Fatalf("close counts = interface %d, device %d", nativeInterface.closes, native.closes)
	}
}

func TestDarwinClaimRejectsAClosedDevice(t *testing.T) {
	nativeInterface := &fakeDarwinInterface{}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}
	if err := device.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := device.ClaimInterface(0); !errors.Is(err, errDarwinDeviceClosed) {
		t.Fatalf("ClaimInterface error = %v, want %v", err, errDarwinDeviceClosed)
	}
	if len(native.interfaces) != 0 {
		t.Fatalf("native interface selections = %#v, want none", native.interfaces)
	}
}

func TestDarwinAlternateSettingReplacesPipeRoutes(t *testing.T) {
	nativeInterface := &fakeDarwinInterface{pipesValue: []darwinPipe{
		{
			endpoint:     0x01,
			ref:          1,
			transferType: darwinBulkPipe,
		},
	}}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}
	claim, err := device.ClaimInterface(0)
	if err != nil {
		t.Fatalf("ClaimInterface: %v", err)
	}
	nativeInterface.pipesValue = []darwinPipe{
		{
			endpoint:     0x82,
			ref:          3,
			transferType: darwinBulkPipe,
		},
	}
	if err := claim.SetAltSetting(2); err != nil {
		t.Fatalf("SetAltSetting: %v", err)
	}
	if !reflect.DeepEqual(nativeInterface.alternates, []uint8{2}) {
		t.Fatalf("alternate selections = %#v", nativeInterface.alternates)
	}
	if _, ok := device.routes[0x01]; ok {
		t.Fatal("old endpoint route remained")
	}
	if got := device.routes[0x82]; got.ref != 3 {
		t.Fatalf("new endpoint route = %#v", got)
	}
}

func TestDarwinAlternateSettingInvalidatesRoutesOnFailure(t *testing.T) {
	want := errors.New("alternate failed")
	nativeInterface := &fakeDarwinInterface{pipesValue: []darwinPipe{
		{
			endpoint:     0x01,
			ref:          1,
			transferType: darwinBulkPipe,
		},
	}}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}
	claim, err := device.ClaimInterface(0)
	if err != nil {
		t.Fatalf("ClaimInterface: %v", err)
	}
	claim.altKnown = true
	claim.endpoints = map[uint8]Endpoint{0x01: {Address: 0x01}}
	nativeInterface.setAltErr = want
	if err := claim.SetAltSetting(1); !errors.Is(err, want) {
		t.Fatalf("SetAltSetting error = %v, want %v", err, want)
	}
	if device.routes != nil {
		t.Fatalf("routes after failure = %#v", device.routes)
	}
	if claim.altKnown || claim.endpoints != nil {
		t.Fatalf("claim cache after failure = known %t endpoints %#v", claim.altKnown, claim.endpoints)
	}
}

func TestDarwinAlternateSettingInvalidatesRoutesWhenPipesFail(t *testing.T) {
	want := errors.New("pipe discovery failed")
	nativeInterface := &fakeDarwinInterface{}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}
	claim, err := device.ClaimInterface(0)
	if err != nil {
		t.Fatalf("ClaimInterface: %v", err)
	}
	claim.altKnown = true
	claim.endpoints = map[uint8]Endpoint{0x01: {Address: 0x01}}
	nativeInterface.pipesErr = want
	if err := claim.SetAltSetting(1); !errors.Is(err, want) {
		t.Fatalf("SetAltSetting error = %v, want %v", err, want)
	}
	if device.routes != nil {
		t.Fatalf("routes after failure = %#v", device.routes)
	}
	if claim.altKnown || claim.endpoints != nil {
		t.Fatalf("claim cache after failure = known %t endpoints %#v", claim.altKnown, claim.endpoints)
	}
}
