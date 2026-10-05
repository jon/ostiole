//go:build linux && (ppc64 || ppc64le || mips || mipsle || mips64 || mips64le)

package usb

const (
	usbfsIOCNone  = uintptr(0x20000000)
	usbfsIOCRead  = uintptr(0x40000000)
	usbfsIOCWrite = uintptr(0x80000000)
)
