package usb

import "testing"

func TestDeviceFiltersExpressAllExactAndVendorIntent(t *testing.T) {
	all := AllDevices()
	if !all.matches(DeviceInfo{VID: 0x1234, PID: 0xabcd}) || !all.matches(DeviceInfo{VID: 0x5678, PID: 0}) {
		t.Fatal("AllDevices() did not match every device")
	}

	exactZero := ExactDevice(0x1234, 0)
	if !exactZero.matches(DeviceInfo{VID: 0x1234, PID: 0}) {
		t.Fatal("ExactDevice() did not match product zero")
	}
	if exactZero.matches(DeviceInfo{VID: 0x1234, PID: 1}) {
		t.Fatal("ExactDevice() matched another product")
	}

	vendor := VendorDevices(0x1234)
	if !vendor.matches(DeviceInfo{VID: 0x1234, PID: 0xabcd}) {
		t.Fatal("VendorDevices() did not match the selected vendor")
	}
	if vendor.matches(DeviceInfo{VID: 0x5678, PID: 0xabcd}) {
		t.Fatal("VendorDevices() matched another vendor")
	}

	if (DeviceFilter{}).valid() {
		t.Fatal("zero DeviceFilter is valid")
	}
}
