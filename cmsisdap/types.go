package cmsisdap

import "github.com/jon/ostiole/usb"

// Capability identifies one CMSIS-DAP capability bit.
type Capability uint8

const (
	CapabilitySWD Capability = iota
	CapabilityJTAG
	CapabilitySWOUART
	CapabilitySWOManchester
	CapabilityAtomicCommands
	CapabilityTestDomainTimer
	CapabilitySWOStreaming
	CapabilityUART
	CapabilityUSBComPort
)

// Capabilities is the probe's CMSIS-DAP capability bitset.
type Capabilities struct {
	bytes []byte
}

// Has reports whether one capability is present.
func (c Capabilities) Has(capability Capability) bool {
	bit := int(capability)
	return bit < len(c.bytes)*8 && c.bytes[bit/8]&(1<<uint(bit%8)) != 0
}

// Bytes returns a copy of the capability bitset.
func (c Capabilities) Bytes() []byte { return append([]byte(nil), c.bytes...) }

// Info is a detached snapshot of probe and session metadata.
type Info struct {
	USB                              usb.DeviceInfo
	Vendor, Product, Serial          string
	ProtocolVersion, FirmwareVersion string
	Capabilities                     Capabilities
	PacketSize, PacketCount          int
}

func cloneInfo(info Info) Info {
	info.Capabilities.bytes = append([]byte(nil), info.Capabilities.bytes...)
	return info
}
