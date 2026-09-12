// Package armdebug owns a probe, its connected Arm debug port, and acquired MEM-APs.
// Clients borrowed from a Conn share its lifetime and must be used serially.
package armdebug

import (
	"context"
	"errors"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/probe"
)

// Conn owns a probe, debug-port state, and MEM-APs acquired through OpenMemAP. Do not copy
// it or use its transferred probe directly. Calls and borrowed clients must be
// serialized. Close owns release; borrowers must not reconnect or release the
// port themselves and must stop using retained pointers once cleanup begins.
type Conn struct {
	probe          *probe.Probe
	port           *dap.DebugPort
	info           probe.Info
	closing        bool
	memories       []ownedMemAP
	cleanupTimeout time.Duration
}

// Connect takes responsibility for opened on entry, even on invalid input.
// It activates the configured wire and connects DAP, which owns protocol entry.
// Failed setup tries
// cleanup; a non-nil result with an error must be retained for Close retries.
// The supplied probe must not already have an activated protocol surface.
func Connect(ctx context.Context, opened *probe.Probe, config Config) (*Conn, error) {
	if opened == nil {
		return nil, errors.New("armdebug: nil probe")
	}
	c := &Conn{probe: opened, info: opened.Info(), cleanupTimeout: config.cleanupTimeout()}
	if err := config.validate(ctx); err != nil {
		return c.fail(err)
	}
	port, err := config.Port.bind(ctx, opened)
	if err != nil {
		return c.fail(err)
	}
	c.port = dap.NewDebugPort(port, config.DAPOptions...)
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

// Close releases owned MEM-APs in reverse acquisition order, then DAP and its wire,
// before closing the probe. It stops on failure,
// retaining dependencies for retry. Each release gets a fresh context bounded
// by Config.CleanupTimeout; lower-layer recovery and host cleanup have their own bounds.
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
		ctx, cancel := c.cleanupContext()
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

func (c *Conn) cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.cleanupTimeout)
}
