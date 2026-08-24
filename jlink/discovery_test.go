package jlink

import (
	"reflect"
	"testing"

	"github.com/jon/ostiole/usb"
)

func TestSupportedDevicesUsesTheReviewedExactCatalog(t *testing.T) {
	want := []uint16{
		0x0101, 0x0102, 0x0103, 0x0104, 0x0105, 0x0107, 0x0108,
		0x1010, 0x1011, 0x1012, 0x1013, 0x1014, 0x1015, 0x1016, 0x1017, 0x1018,
		0x1020, 0x1024, 0x1025,
		0x1050, 0x1051, 0x1052, 0x1053, 0x1054, 0x1055, 0x1056, 0x1057, 0x1058, 0x1059,
		0x1060, 0x1061, 0x1062, 0x1063, 0x1064, 0x1065, 0x1066, 0x1067, 0x1068, 0x1069,
	}
	if !reflect.DeepEqual(supportedPIDs, want) || len(SupportedDevices()) != len(want) {
		t.Fatalf("supported PIDs = %#v", supportedPIDs)
	}
	for _, pid := range want {
		if !supportedDevice(usb.DeviceInfo{VID: VID, PID: pid}) {
			t.Fatalf("PID %#04x is not supported", pid)
		}
	}
	for _, pid := range []uint16{0x0106, 0x1008, 0x1019, 0x1021, 0xffff} {
		if supportedDevice(usb.DeviceInfo{VID: VID, PID: pid}) {
			t.Fatalf("PID %#04x is supported", pid)
		}
	}
	if supportedDevice(usb.DeviceInfo{VID: 0x0403, PID: 0x1020}) {
		t.Fatal("another vendor is supported")
	}
}

func TestFindApplicationInterfaceUsesTheUnambiguousDescriptorPair(t *testing.T) {
	configuration := usb.Configuration{Value: 1, Interfaces: []usb.Interface{
		{Number: 0, Alternates: []usb.AlternateSetting{{
			Number: 0, Class: 2, Subclass: 2, Protocol: 1,
			Endpoints: []usb.Endpoint{{Address: 0x82, TransferType: usb.TransferBulk, MaxPacketSize: 64}},
		}}},
		{Number: 3, Alternates: []usb.AlternateSetting{
			{Number: 0, Class: 0xff, Subclass: 0xff, Protocol: 0xfe},
			{Number: 2, Class: 0xff, Subclass: 0xff, Protocol: 0xff, Endpoints: []usb.Endpoint{
				{Address: 0x84, TransferType: usb.TransferBulk, MaxPacketSize: 512},
				{Address: 0x03, TransferType: usb.TransferBulk, MaxPacketSize: 512},
			}},
		}},
	}}
	got, err := findApplicationInterface(configuration)
	if err != nil {
		t.Fatal(err)
	}
	want := applicationInterface{
		number: 3, alternate: 2,
		bulkIn:  usb.Endpoint{Address: 0x84, TransferType: usb.TransferBulk, MaxPacketSize: 512},
		bulkOut: usb.Endpoint{Address: 0x03, TransferType: usb.TransferBulk, MaxPacketSize: 512},
	}
	if got != want {
		t.Fatalf("application interface = %#v, want %#v", got, want)
	}
}

func TestFindApplicationInterfaceRejectsUnsafeLayouts(t *testing.T) {
	valid := usb.AlternateSetting{Number: 0, Class: 0xff, Subclass: 0xff, Protocol: 0xff, Endpoints: []usb.Endpoint{
		{Address: 0x81, TransferType: usb.TransferBulk, MaxPacketSize: 64},
		{Address: 0x02, TransferType: usb.TransferBulk, MaxPacketSize: 64},
	}}
	extra := valid
	extra.Endpoints = append(append([]usb.Endpoint(nil), valid.Endpoints...), usb.Endpoint{Address: 0x83, TransferType: usb.TransferInterrupt, MaxPacketSize: 8})
	duplicate := valid
	duplicate.Endpoints = []usb.Endpoint{
		{Address: 0x81, TransferType: usb.TransferBulk, MaxPacketSize: 64},
		{Address: 0x82, TransferType: usb.TransferBulk, MaxPacketSize: 64},
	}
	inputZero := valid
	inputZero.Endpoints = append([]usb.Endpoint(nil), valid.Endpoints...)
	inputZero.Endpoints[0].Address = 0x80
	outputZero := valid
	outputZero.Endpoints = append([]usb.Endpoint(nil), valid.Endpoints...)
	outputZero.Endpoints[1].Address = 0x00
	tests := []struct {
		name          string
		configuration usb.Configuration
	}{
		{name: "missing", configuration: usb.Configuration{}},
		{name: "ambiguous", configuration: usb.Configuration{Interfaces: []usb.Interface{
			{Number: 0, Alternates: []usb.AlternateSetting{valid}},
			{Number: 1, Alternates: []usb.AlternateSetting{valid}},
		}}},
		{name: "extra endpoint", configuration: configurationWithAlternate(extra)},
		{name: "duplicate direction", configuration: configurationWithAlternate(duplicate)},
		{name: "input endpoint zero", configuration: configurationWithAlternate(inputZero)},
		{name: "output endpoint zero", configuration: configurationWithAlternate(outputZero)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := findApplicationInterface(test.configuration); err == nil {
				t.Fatal("findApplicationInterface() succeeded")
			}
		})
	}
}

func configurationWithAlternate(alternate usb.AlternateSetting) usb.Configuration {
	return usb.Configuration{Interfaces: []usb.Interface{{Number: 0, Alternates: []usb.AlternateSetting{alternate}}}}
}
