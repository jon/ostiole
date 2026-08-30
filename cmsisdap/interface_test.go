package cmsisdap

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jon/ostiole/usb"
)

func TestFindCommandInterfaceRequiresExactV2Layout(t *testing.T) {
	out := usb.Endpoint{Address: 0x01, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	in := usb.Endpoint{Address: 0x82, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	swo := usb.Endpoint{Address: 0x83, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	configuration := usb.Configuration{Value: 1, Interfaces: []usb.Interface{{
		Number: 4, Alternates: []usb.AlternateSetting{{
			Number: 2, Class: 0xff, Endpoints: []usb.Endpoint{out, in, swo},
		}},
	}}}

	got, err := findCommandInterface(configuration)
	if err != nil {
		t.Fatal(err)
	}
	want := commandInterface{number: 4, alternate: 2, bulkOut: out, bulkIn: in, swoIn: swo}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findCommandInterface() = %#v, want %#v", got, want)
	}
}

func TestFindCommandInterfaceRejectsNearMatches(t *testing.T) {
	out := usb.Endpoint{Address: 0x01, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	in := usb.Endpoint{Address: 0x82, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	interrupt := usb.Endpoint{Address: 0x82, TransferType: usb.TransferInterrupt, MaxPacketSize: 64}
	tests := []struct {
		name      string
		alternate usb.AlternateSetting
	}{
		{name: "HID v1", alternate: usb.AlternateSetting{Class: 0x03, Endpoints: []usb.Endpoint{out, interrupt}}},
		{name: "wrong subclass", alternate: usb.AlternateSetting{Class: 0xff, Subclass: 3, Endpoints: []usb.Endpoint{out, in}}},
		{name: "wrong protocol", alternate: usb.AlternateSetting{Class: 0xff, Protocol: 1, Endpoints: []usb.Endpoint{out, in}}},
		{name: "reversed endpoints", alternate: usb.AlternateSetting{Class: 0xff, Endpoints: []usb.Endpoint{in, out}}},
		{name: "missing IN", alternate: usb.AlternateSetting{Class: 0xff, Endpoints: []usb.Endpoint{out}}},
		{name: "extra endpoint", alternate: usb.AlternateSetting{Class: 0xff, Endpoints: []usb.Endpoint{out, in, in, in}}},
		{name: "invalid SWO", alternate: usb.AlternateSetting{Class: 0xff, Endpoints: []usb.Endpoint{out, in, interrupt}}},
		{name: "duplicate SWO", alternate: usb.AlternateSetting{Class: 0xff, Endpoints: []usb.Endpoint{out, in, in}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := usb.Configuration{Interfaces: []usb.Interface{{Alternates: []usb.AlternateSetting{test.alternate}}}}
			_, err := findCommandInterface(configuration)
			if !errors.Is(err, ErrNoV2Interface) {
				t.Fatalf("findCommandInterface() error = %v, want ErrNoV2Interface", err)
			}
		})
	}
}

func TestFindCommandInterfaceRejectsAmbiguity(t *testing.T) {
	out := usb.Endpoint{Address: 0x01, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	in := usb.Endpoint{Address: 0x82, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	alternate := usb.AlternateSetting{Class: 0xff, Endpoints: []usb.Endpoint{out, in}}
	configuration := usb.Configuration{Interfaces: []usb.Interface{
		{Number: 1, Alternates: []usb.AlternateSetting{alternate}},
		{Number: 2, Alternates: []usb.AlternateSetting{alternate}},
	}}

	_, err := findCommandInterface(configuration)
	if err == nil || errors.Is(err, ErrNoV2Interface) {
		t.Fatalf("findCommandInterface() error = %v, want ambiguity", err)
	}
}
