package ftdi

import (
	"reflect"
	"testing"

	"github.com/jon/ostiole/usb"
)

func TestSupportedDevicesListsOnlyMPSSEProducts(t *testing.T) {
	want := []usb.DeviceFilter{
		usb.ExactDevice(VID, PIDFT2232H),
		usb.ExactDevice(VID, PIDFT4232H),
		usb.ExactDevice(VID, PIDFT232H),
	}
	if got := SupportedDevices(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedDevices() = %#v, want %#v", got, want)
	}
}
