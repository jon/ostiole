// Package cortexm identifies Cortex-M processors through target memory.
package cortexm

import (
	"context"
	"errors"
	"fmt"
)

const cpuidAddress = uint32(0xe000ed00)

// WordReader reads one aligned 32-bit target word.
type WordReader interface {
	ReadWord(context.Context, uint32) (uint32, error)
}

// Identity contains the structural fields of a Cortex-M CPUID value.
type Identity struct {
	Raw          uint32
	Implementer  uint8
	Variant      uint8
	Architecture uint8
	Part         uint16
	Revision     uint8
}

// Identify reads and validates the architectural Cortex-M CPUID register.
func Identify(ctx context.Context, reader WordReader) (Identity, error) {
	if reader == nil {
		return Identity{}, errors.New("cortexm: nil word reader")
	}
	raw, err := reader.ReadWord(ctx, cpuidAddress)
	if err != nil {
		return Identity{}, fmt.Errorf("cortexm: read CPUID: %w", err)
	}
	info := Identity{
		Raw:          raw,
		Implementer:  uint8(raw >> 24),
		Variant:      uint8(raw >> 20 & 0x0f),
		Architecture: uint8(raw >> 16 & 0x0f),
		Part:         uint16(raw >> 4 & 0x0fff),
		Revision:     uint8(raw & 0x0f),
	}
	if info.Implementer != 0x41 || info.Part == 0 {
		return Identity{}, fmt.Errorf(
			"cortexm: CPUID %#08x is not a plausible Cortex-M identity",
			raw,
		)
	}
	return info, nil
}
