// Package sim models Cortex-M0 and Secure Cortex-M33 debug registers behind
// dap/sim.MemoryDevice. It models hardware state, not debugger ownership.
package sim

import (
	"context"
	"encoding/binary"
	"errors"

	dapsim "github.com/jon/ostiole/dap/sim"
)

// Profile selects one implemented processor and debug-access environment.
// Zero is invalid. M33 models Secure execution and privileged Secure DAP access.
type Profile uint8

const (
	// M0 models the Cortex-M0 identity used by the micro:bit bench.
	M0 Profile = iota + 1
	// M33 models the Cortex-M33 identity used by the RP2350 bench.
	M33
)

// Snapshot is a detached view of raw debug registers. Obtaining it has no bus
// traffic or read-to-clear effects. Initial supplies fixture values for UNKNOWN
// fields; unspecified fields use zero.
type Snapshot struct {
	DHCSR uint32
	DFSR  uint32
}

// Config fixes a core's profile and initial debug state. Initial accepts the
// modeled DHCSR control/status and five DFSR reason bits; reserved bits fail.
// Secure debug permission is represented by DHCSR.S_SDE for M33 only.
type Config struct {
	Profile Profile
	Initial Snapshot
}

// Core supplies absolute-address, little-endian word accesses to CPUID, DHCSR
// and DFSR. Map its register windows before DAP traffic. Serialize all accesses
// and fixture operations, including across AP views. Do not copy Core.
// The zero value is invalid.
// It owns no transport or cleanup and never assigns a debugger's halt claim.
type Core struct {
	profile      Profile
	state        Snapshot
	unsafeMemory bool
}

const (
	enabled        = uint32(1)
	haltRequest    = uint32(2)
	step           = uint32(4)
	maskInterrupts = uint32(8)
	snapStall      = uint32(32)
	registerReady  = uint32(1 << 16)
	inDebug        = uint32(1 << 17)
	secureDebug    = uint32(1 << 20)
	retired        = uint32(1 << 24)
	reset          = uint32(1 << 25)
	restarted      = uint32(1 << 26)
	sticky         = retired | reset | restarted
)

// New constructs a core without target traffic or cleanup obligations.
func New(cfg Config) (*Core, error) {
	mask := enabled | haltRequest | maskInterrupts | registerReady | inDebug | retired | reset
	if cfg.Profile == M33 {
		mask |= secureDebug | restarted | snapStall
	}
	if cfg.Profile != M0 && cfg.Profile != M33 || cfg.Initial.DHCSR & ^mask != 0 || cfg.Initial.DFSR & ^uint32(31) != 0 {
		return nil, errors.New("cortexm/sim: unsupported profile or initial register state")
	}
	if cfg.Initial.DHCSR&inDebug != 0 && (cfg.Initial.DHCSR&(enabled|haltRequest) != enabled|haltRequest || cfg.Profile == M33 && cfg.Initial.DHCSR&secureDebug == 0) {
		return nil, errors.New("cortexm/sim: unsupported inherited Debug state")
	}
	return &Core{
		profile: cfg.Profile, state: cfg.Initial,
		unsafeMemory: cfg.Initial.DHCSR&snapStall != 0,
	}, nil
}

// Snapshot returns current fixture state without consuming sticky status.
func (c *Core) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	return c.state
}

func (c *Core) validate(ctx context.Context, addr uint64, data []byte) error {
	if ctx == nil {
		return errors.New("cortexm/sim: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.profile == 0 {
		return errors.New("cortexm/sim: invalid core")
	}
	if len(data) != 4 || addr%4 != 0 {
		return dapsim.ErrBusFault
	}
	return nil
}

// Read implements dap/sim.MemoryDevice. Only aligned words are supported.
// DHCSR reads consume reset, retirement and M33 restart sticky status.
func (c *Core) Read(ctx context.Context, addr uint64, data []byte) error {
	if err := c.validate(ctx, addr, data); err != nil {
		return err
	}
	var value uint32
	switch addr {
	case 0xe000ed00:
		value = 0x410cc200
		if c.profile == M33 {
			value = 0x411fd210
		}
	case 0xe000edf0:
		value = c.state.DHCSR
		c.state.DHCSR &^= sticky
	case 0xe000ed30:
		value = c.state.DFSR
	default:
		return dapsim.ErrBusFault
	}
	binary.LittleEndian.PutUint32(data, value)
	return nil
}

// Write implements dap/sim.MemoryDevice. DFSR is write-one-to-clear. DHCSR
// requires DEBUGKEY; unsupported execution or unpredictable control changes
// return ErrUnsupported before effects. Unknown register addresses bus-fault.
func (c *Core) Write(ctx context.Context, addr uint64, data []byte) error {
	if err := c.validate(ctx, addr, data); err != nil {
		return err
	}
	switch addr {
	case 0xe000ed30:
		c.state.DFSR &^= binary.LittleEndian.Uint32(data) & 31
		return nil
	case 0xe000edf0:
		return c.writeControl(binary.LittleEndian.Uint32(data))
	default:
		return dapsim.ErrBusFault
	}
}
