// Package armdebug owns a probe, its connected Arm debug port, and acquired MEM-APs.
// Clients borrowed from a Conn share its lifetime and must be used serially.
package armdebug

import (
	"context"
	"errors"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/swd"
)

// PortConfig selects and configures an Arm debug port. Its zero value is invalid.
type PortConfig struct {
	swd   probe.SWDConfig
	valid bool
}

// SWDP describes an SW-DP without traffic. Connect validates the clock ceiling.
func SWDP(config probe.SWDConfig) PortConfig {
	return PortConfig{swd: config, valid: true}
}

// Config supplies an explicit port configuration and existing DAP options.
type Config struct {
	Port       PortConfig
	DAPOptions []dap.Option
}

func (c Config) validate(ctx context.Context) error {
	if ctx == nil {
		return errors.New("armdebug: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.Port.valid || c.Port.swd.MaxClockHz < 1000 {
		return errors.New("armdebug: explicit SW-DP clock ceiling of at least 1 kHz required")
	}
	return nil
}

// Conn owns a probe, debug-port state, and MEM-APs acquired through OpenMemAP. Do not copy
// it or use its transferred probe directly. Calls and borrowed clients must be
// serialized. Close owns release; borrowers must not reconnect or release the
// port themselves and must stop using retained pointers once cleanup begins.
type Conn struct {
	probe    *probe.Probe
	port     *dap.DebugPort
	info     probe.Info
	closing  bool
	memories []ownedMemAP
}

// Connect takes responsibility for opened on entry, even on invalid input.
// It configures SWD and connects DAP, which owns SWD entry. Failed setup tries
// cleanup; a non-nil result with an error must be retained for Close retries.
// The supplied probe must not already have an activated protocol surface.
func Connect(ctx context.Context, opened *probe.Probe, config Config) (*Conn, error) {
	if opened == nil {
		return nil, errors.New("armdebug: nil probe")
	}
	c := &Conn{probe: opened, info: opened.Info()}
	if err := config.validate(ctx); err != nil {
		return c.fail(err)
	}
	wire, err := opened.SWD(ctx, config.Port.swd)
	if err != nil {
		return c.fail(err)
	}
	c.port = dap.NewDebugPort(dap.SWDP(swd.New(wire)), config.DAPOptions...)
	if _, err := c.port.Connect(ctx); err != nil {
		return c.fail(err)
	}
	return c, nil
}

func (c *Conn) fail(cause error) (*Conn, error) {
	if err := c.Close(); err != nil {
		return c, errors.Join(cause, err)
	}
	return nil, cause
}

// Info returns detached probe metadata, including after closing.
func (c *Conn) Info() probe.Info {
	if c == nil {
		return probe.Info{}
	}
	return c.info
}

// Port lends the connected DAP client, or nil once cleanup starts. The caller
// must not reconnect or release it. Retained pointers are not forcibly revoked.
// Manually acquired MEM-AP clients must be released before closing this owner.
func (c *Conn) Port() *dap.DebugPort {
	if c == nil || c.closing {
		return nil
	}
	return c.port
}

// Close releases owned MEM-APs in reverse acquisition order, then DAP and SWD,
// before closing the probe. It stops on failure,
// retaining dependencies for retry. Protocol release gets a fresh one-second
// context; lower-layer recovery and host cleanup have their own bounds.
// There is no forced abandonment. Closing a nil or already closed Conn does nothing.
func (c *Conn) Close() error {
	if c == nil {
		return nil
	}
	c.closing = true
	if err := c.releaseMemAPs(); err != nil {
		return err
	}
	if c.port != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := c.port.Release(ctx)
		cancel()
		if err != nil {
			return err
		}
		c.port = nil
	}
	if c.probe != nil {
		if err := c.probe.Close(); err != nil {
			return err
		}
		c.probe = nil
	}
	return nil
}
