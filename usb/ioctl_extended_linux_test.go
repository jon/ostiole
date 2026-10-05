//go:build linux && (ppc64 || ppc64le || mips || mipsle || mips64 || mips64le)

package usb

import "unsafe"

const (
	expectedControl   = uintptr(0xc0105500) + (unsafe.Sizeof(uintptr(0))/8)*0x80000
	expectedSubmitURB = uintptr(0x402c550a) + (unsafe.Sizeof(uintptr(0))/8)*0xc0000
	expectedReapURB   = uintptr(0x8000550d) + unsafe.Sizeof(uintptr(0))*0x10000
)

var (
	_ [0 - int(usbfsControl^expectedControl)]byte
	_ [0 - int(usbfsClaimInterface^uintptr(0x4004550f))]byte
	_ [0 - int(usbfsReleaseInterface^uintptr(0x40045510))]byte
	_ [0 - int(usbfsSetInterface^uintptr(0x40085504))]byte
	_ [0 - int(usbfsDiscardURB^uintptr(0x2000550b))]byte
	_ [0 - int(usbfsSubmitURB^expectedSubmitURB)]byte
	_ [0 - int(usbfsReapURBNoDelay^expectedReapURB)]byte
)
