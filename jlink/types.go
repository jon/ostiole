package jlink

import (
	"errors"
	"fmt"
	"unicode/utf8"

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

// WithSWD configures Open to select SWD and request a whole-kHz target clock no
// greater than maxClockHz after reading probe metadata. The request must be at
// least 1 kHz.
func WithSWD(maxClockHz uint32) Option {
	return Option{apply: func(config *openConfig) error {
		if maxClockHz < 1_000 {
			return errors.New("SWD clock ceiling must be at least 1 kHz")
		}
		config.configureSWD = true
		config.maxClockHz = maxClockHz
		return nil
	}}
}

// HardwareVersion is the decimal-packed probe hardware version.
type HardwareVersion struct {
	Raw                          uint32
	Type, Major, Minor, Revision uint8
}

// Capabilities is the probe's opaque capability bitset.
type Capabilities struct {
	bytes []byte
}

// Has reports whether one capability bit is set.
func (c Capabilities) Has(bit int) bool {
	return bit >= 0 && bit < len(c.bytes)*8 && c.bytes[bit/8]&(1<<uint(bit%8)) != 0
}

// Bytes returns a copy of the capability bitset.
func (c Capabilities) Bytes() []byte { return append([]byte(nil), c.bytes...) }

// BitLen reports the number of represented capability bits.
func (c Capabilities) BitLen() int { return len(c.bytes) * 8 }

// Info is a detached snapshot of probe and session metadata.
type Info struct {
	USB                    usb.DeviceInfo
	Firmware               string
	FirmwareRecord         []byte
	Hardware               HardwareVersion
	HardwareKnown          bool
	Capabilities           Capabilities
	Workspace              uint32
	WorkspaceKnown         bool
	AvailableInterfaces    uint32
	SelectedInterface      uint8
	SelectedInterfaceKnown bool
}

func cloneInfo(info Info) Info {
	info.FirmwareRecord = append([]byte(nil), info.FirmwareRecord...)
	info.Capabilities.bytes = append([]byte(nil), info.Capabilities.bytes...)
	return info
}

func decodeHardwareVersion(raw uint32) HardwareVersion {
	return HardwareVersion{
		Raw: raw, Type: uint8(raw / 1_000_000 % 100), Major: uint8(raw / 10_000 % 100),
		Minor: uint8(raw / 100 % 100), Revision: uint8(raw % 100),
	}
}

func firmwareString(record []byte) string {
	for index, value := range record {
		if value == 0 {
			record = record[:index]
			break
		}
	}
	result := make([]byte, 0, len(record))
	for len(record) != 0 {
		value, size := utf8.DecodeRune(record)
		if value == utf8.RuneError && size == 1 {
			result = fmt.Appendf(result, "\\x%02x", record[0])
		} else {
			result = append(result, record[:size]...)
		}
		record = record[size:]
	}
	return string(result)
}
