package cmsisdap

import (
	"errors"

	"github.com/jon/ostiole/usb"
)

// Option configures Open. Its zero value is ignored.
type Option struct {
	apply func(*openConfig) error
}

type openConfig struct {
	configureSWD bool
	maxClockHz   uint32
}

// WithSWD configures Open to connect the SWD port and request a maximum target
// clock after reading probe metadata.
func WithSWD(maxClockHz uint32) Option {
	return Option{apply: func(config *openConfig) error {
		if maxClockHz == 0 {
			return errors.New("SWD clock ceiling must be greater than zero")
		}
		config.configureSWD = true
		config.maxClockHz = maxClockHz
		return nil
	}}
}

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
