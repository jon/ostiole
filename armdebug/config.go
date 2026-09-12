package armdebug

import (
	"context"
	"errors"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/swd"
)

// PortConfig selects and configures an Arm debug port. Its zero value is invalid.
type PortConfig struct {
	swd      probe.SWDConfig
	jtag     probe.JTAGConfig
	layout   jtag.Layout
	tapIndex int
	isJTAG   bool
	valid    bool
}

// SWDP describes an SW-DP without traffic. Connect validates the clock ceiling.
func SWDP(config probe.SWDConfig) PortConfig {
	return PortConfig{swd: config, valid: true}
}

// JTAGDP describes a baseline ADIv5 JTAG-DP without traffic. It copies the
// complete expected layout; tapIndex is zero-based, nearest TDO first.
// Open and Connect validate the clock, layout, and selected TAP before
// activation. The TAP must have an IDCODE and a four- or eight-bit IR.
// Board-specific chain routing must be enabled
// externally; physical chain validation belongs to DAP connection setup.
func JTAGDP(config probe.JTAGConfig, layout jtag.Layout, tapIndex int) PortConfig {
	return PortConfig{jtag: config, layout: append(jtag.Layout(nil), layout...), tapIndex: tapIndex, isJTAG: true, valid: true}
}

// Config supplies an explicit port configuration and existing DAP options.
type Config struct {
	Port PortConfig
	// DAPOptions are validated by DAP after probe activation.
	DAPOptions []dap.Option

	// CleanupTimeout bounds each owned MEM-AP or debug-port release attempt.
	// Zero selects one second for SWD or thirty seconds for JTAG. Negative
	// values are invalid. DAPOptions can separately configure the independent
	// recovery attempts made inside DAP; host cleanup retains its own bounds.
	CleanupTimeout time.Duration
}

func (c Config) validate(ctx context.Context) error {
	if ctx == nil {
		return errors.New("armdebug: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.CleanupTimeout < 0 {
		return errors.New("armdebug: negative cleanup timeout")
	}
	return c.Port.validate()
}

func (p PortConfig) validate() error {
	clock := p.swd.MaxClockHz
	if p.isJTAG {
		clock = p.jtag.MaxClockHz
	}
	if !p.valid || clock < 1000 {
		return errors.New("armdebug: explicit debug-port clock ceiling of at least 1 kHz required")
	}
	if !p.isJTAG {
		return nil
	}
	if err := p.layout.Validate(); err != nil {
		return err
	}
	if p.tapIndex < 0 || p.tapIndex >= len(p.layout) {
		return errors.New("armdebug: JTAG TAP index outside the expected chain")
	}
	tap := p.layout[p.tapIndex]
	if tap.ResetRegister().Bypass || tap.IRBits() != 4 && tap.IRBits() != 8 {
		return errors.New("armdebug: JTAG-DP requires a TAP with an IDCODE and a four- or eight-bit IR")
	}
	return nil
}

func (c Config) cleanupTimeout() time.Duration {
	if c.CleanupTimeout != 0 {
		return c.CleanupTimeout
	}
	if c.Port.isJTAG {
		return 30 * time.Second
	}
	return time.Second
}

func (p PortConfig) bind(ctx context.Context, opened *probe.Probe) (dap.Port, error) {
	if !p.isJTAG {
		wire, err := opened.SWD(ctx, p.swd)
		return dap.SWDP(swd.New(wire)), err
	}
	wire, err := opened.JTAG(ctx, p.jtag)
	if err != nil {
		return dap.Port{}, err
	}
	chain, err := jtag.NewChain(jtag.New(wire), p.layout)
	if err != nil {
		return dap.Port{}, err
	}
	return dap.JTAGDP(chain, p.tapIndex), nil
}
