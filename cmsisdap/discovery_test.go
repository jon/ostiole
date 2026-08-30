package cmsisdap

import (
	"reflect"
	"testing"

	"github.com/jon/ostiole/usb"
)

func TestCandidatesUseCaseSensitiveProductMarker(t *testing.T) {
	devices := []usb.DeviceInfo{
		{VID: 0x0d28, PID: 0x0204, Bus: 1, Address: 2, Product: "DAPLink CMSIS-DAP", Serial: "first"},
		{VID: 0x1234, PID: 0x5678, Bus: 1, Address: 3, Product: "CMSIS-DAP v2", Serial: "second"},
		{VID: 0x1234, PID: 0x5679, Bus: 1, Address: 4, Product: "cmsis-dap"},
		{VID: 0x0403, PID: 0x6014, Bus: 1, Address: 5, Product: "FT232H"},
	}

	got := Candidates(devices)
	want := devices[:2]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Candidates() = %#v, want %#v", got, want)
	}
	got[0].Product = "changed"
	if devices[0].Product == "changed" {
		t.Fatal("Candidates() returned storage backed by its input")
	}
}
