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
	closeErr   error
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

	if err := device.ClaimInterface(0); err != nil {
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
	if err := device.ClaimInterface(1); err == nil {
		t.Fatal("second ClaimInterface succeeded")
	}
	if err := device.ReleaseInterface(1); err == nil {
		t.Fatal("ReleaseInterface for unclaimed interface succeeded")
	}
	if err := device.ReleaseInterface(0); err != nil {
		t.Fatalf("ReleaseInterface: %v", err)
	}
	if nativeInterface.closes != 1 {
		t.Fatalf("interface closes = %d, want 1", nativeInterface.closes)
	}
}

func TestDarwinClaimCleansUpAfterPipeDiscoveryFailure(t *testing.T) {
	want := errors.New("pipe discovery failed")
	nativeInterface := &fakeDarwinInterface{pipesErr: want}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}

	if err := device.ClaimInterface(0); !errors.Is(err, want) {
		t.Fatalf("ClaimInterface error = %v, want %v", err, want)
	}
	if nativeInterface.closes != 1 {
		t.Fatalf("interface closes = %d, want 1", nativeInterface.closes)
	}
	if device.hasClaim {
		t.Fatal("failed claim remained active")
	}
}

func TestDarwinClaimCleansUpAfterSeizureFailure(t *testing.T) {
	want := errors.New("seizure failed")
	nativeInterface := &fakeDarwinInterface{openErr: want}
	native := &fakeDarwinInterfaceDevice{iface: nativeInterface}
	device := &Device{handle: native}

	if err := device.ClaimInterface(0); !errors.Is(err, want) {
		t.Fatalf("ClaimInterface error = %v, want %v", err, want)
	}
	if nativeInterface.closes != 1 {
		t.Fatalf("interface closes = %d, want 1", nativeInterface.closes)
	}
	if device.hasClaim {
		t.Fatal("failed claim remained active")
	}
}

func TestDarwinCloseJoinsInterfaceAndDeviceFailures(t *testing.T) {
	wantRelease := errors.New("release failed")
	wantClose := errors.New("device close failed")
	nativeInterface := &fakeDarwinInterface{closeErr: wantRelease}
	native := &fakeDarwinInterfaceDevice{
		fakeDarwinDevice: fakeDarwinDevice{closeErr: wantClose},
		iface:            nativeInterface,
	}
	device := &Device{handle: native}
	if err := device.ClaimInterface(0); err != nil {
		t.Fatalf("ClaimInterface: %v", err)
	}
	err := device.Close()
	if !errors.Is(err, wantRelease) || !errors.Is(err, wantClose) {
		t.Fatalf("Close error = %v", err)
	}
	if err := device.Close(); !errors.Is(err, wantRelease) {
		t.Fatalf("second Close error = %v", err)
	}
	if nativeInterface.closes != 1 || native.closes != 1 {
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
	if err := device.ClaimInterface(0); !errors.Is(err, errDarwinDeviceClosed) {
		t.Fatalf("ClaimInterface error = %v, want %v", err, errDarwinDeviceClosed)
	}
	if len(native.interfaces) != 0 {
		t.Fatalf("native interface selections = %#v, want none", native.interfaces)
	}
}
