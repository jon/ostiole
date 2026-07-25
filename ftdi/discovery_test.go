package ftdi

import (
	"reflect"
	"testing"

	"github.com/jon/ostiole/usb"
)

func TestSupportedDevicesListsOnlyMPSSEProducts(t *testing.T) {
	want := []usb.DeviceFilter{
		{VID: VID, PID: PIDFT2232H},
		{VID: VID, PID: PIDFT4232H},
		{VID: VID, PID: PIDFT232H},
	}
	if got := SupportedDevices(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedDevices() = %#v, want %#v", got, want)
	}
}
