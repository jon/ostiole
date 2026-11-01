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

// ErrRestorationAbandoned reports that terminal probe failure prevented target
// restoration. Host resources may have been released; a halted target can remain
// halted. Another Close may be needed for pending host cleanup.
var ErrRestorationAbandoned = errors.New("armdebug: target restoration abandoned")

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
// It activates the configured protocol surface and connects DAP, which owns
// protocol entry. SWD prefers a register backend when advertised; activation
// failures do not fall back to a wire.
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
	err := c.Close()
	if err != nil && c.probe != nil {
		return c, errors.Join(cause, err)
	}
	return nil, errors.Join(cause, err)
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

// Close releases owned MEM-APs in reverse acquisition order, then DAP and its
// wire, before closing the probe. Live restoration failures retain dependencies
// for retry. If the probe reports restoration impossible, Close abandons pending
// target restoration, returns ErrRestorationAbandoned and continues host cleanup.
// Releasing host resources does not resume a halted target. Each release gets a
// fresh context bounded by Config.CleanupTimeout; lower-layer recovery and host
// cleanup have their own bounds. Host close errors are returned on the attempt
// that encounters them; only pending resources remain owned for retry.
// Closing a nil or fully released Conn does nothing.
func (c *Conn) Close() error {
	if c == nil {
		return nil
	}
	c.closing = true
	err := c.releaseTarget()
	if err != nil && (c.probe == nil || c.probe.RestorationPossible()) {
		return err
	}
	if err != nil && !errors.Is(err, ErrRestorationAbandoned) {
		err = errors.Join(err, ErrRestorationAbandoned)
	}
	c.memories = nil
	c.port = nil
	return errors.Join(err, c.closeProbe())
}

func (c *Conn) releaseTarget() error {
	if c.probe != nil && !c.probe.RestorationPossible() {
		if len(c.memories) != 0 || c.port.CleanupPending() {
			return ErrRestorationAbandoned
		}
		return nil
	}
	if err := c.releaseMemAPs(); err != nil {
		return err
	}
	if c.port != nil {
		ctx, cancel := c.cleanupContext()
		err := c.port.Release(ctx)
		cancel()
		return err
	}
	return nil
}

func (c *Conn) closeProbe() error {
	if c.probe == nil {
		return nil
	}
	err := c.probe.Close()
	if !c.probe.CleanupPending() {
		c.probe = nil
	}
	return err
}

func (c *Conn) cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.cleanupTimeout)
}
