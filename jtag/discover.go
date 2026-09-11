package jtag

import (
	"context"
	"errors"
)

// ErrDiscoveryLimit reports a chain exceeding the caller's bound or TDO stuck low.
var ErrDiscoveryLimit = errors.New("jtag: chain did not terminate within discovery limit")

// ErrNoChain reports an empty scan path or TDO stuck high; these are indistinguishable.
var ErrNoChain = errors.New("jtag: no TAP observed")

// ObservedTAP describes one detached reset data-register observation. Bypass
// means a one-bit zero, not an IDCODE of zero. Order is nearest TDO first.
type ObservedTAP struct {
	IDCODE uint32
	Bypass bool
}

// Discover resets the TAPs and scans their reset-selected IDCODE or BYPASS
// registers. It does not infer IR lengths, recognize boards, or enable DAPs.
// maxTAPs must be in [1, 1024]. A bounded failure returns the observed prefix.
// All-one termination cannot distinguish an open TDO line from an empty path.
func (c *Conn) Discover(ctx context.Context, maxTAPs int) ([]ObservedTAP, error) {
	if _, err := c.validate(ctx); err != nil {
		return nil, err
	}
	if maxTAPs < 1 || maxTAPs > 1024 {
		return nil, errors.New("jtag: invalid discovery bound")
	}
	if err := c.Reset(ctx); err != nil {
		return nil, err
	}
	bits := 32 * (maxTAPs + 1)
	input := make([]byte, bits/8)
	for i := range input {
		input[i] = 0xff
	}
	data, err := c.ScanDR(ctx, input, bits)
	if err != nil {
		return nil, err
	}
	return observations(data, maxTAPs)
}

func observations(data []byte, maxTAPs int) ([]ObservedTAP, error) {
	var found []ObservedTAP
	offset := 0
	for {
		value := word(data, offset)
		if value == ^uint32(0) {
			if len(found) == 0 {
				return nil, ErrNoChain
			}
			return found, nil
		}
		if len(found) == maxTAPs {
			return found, ErrDiscoveryLimit
		}
		if value&1 == 0 {
			found = append(found, ObservedTAP{Bypass: true})
			offset++
		} else {
			found = append(found, ObservedTAP{IDCODE: value})
			offset += 32
		}
	}
}

func word(data []byte, offset int) uint32 {
	var value uint32
	for i := range 32 {
		value |= uint32(get(data, offset+i)) << uint(i)
	}
	return value
}
