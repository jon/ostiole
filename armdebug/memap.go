package armdebug

import (
	"context"
	"errors"

	"github.com/jon/ostiole/dap"
)

type ownedMemAP struct {
	selection dap.APSel
	client    *dap.MemAP
}

// OpenMemAP acquires one explicitly selected memory AP and lends its client.
// Distinct APs may be acquired; acquiring the same AP twice is an error before
// traffic. Close owns release. Do not independently release borrowed clients
// or use them after owner cleanup begins. Calls must be serialized.
// Failure preserves the owner; existing DAP state governs further operations.
func (c *Conn) OpenMemAP(ctx context.Context, ap dap.APSel) (*dap.MemAP, error) {
	if c == nil || c.closing || c.port == nil {
		return nil, errors.New("armdebug: connection is not available for MEM-AP acquisition")
	}
	if ctx == nil {
		return nil, errors.New("armdebug: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, owned := range c.memories {
		if owned.selection == ap {
			return nil, errors.New("armdebug: access port is already owned")
		}
	}
	client, err := dap.OpenMemAP(ctx, c.port, ap)
	if err != nil {
		return nil, err
	}
	c.memories = append(c.memories, ownedMemAP{selection: ap, client: client})
	return client, nil
}

func (c *Conn) releaseMemAPs() error {
	for len(c.memories) != 0 {
		last := len(c.memories) - 1
		ctx, cancel := c.cleanupContext()
		err := c.memories[last].client.Release(ctx)
		cancel()
		if err != nil {
			return err
		}
		c.memories[last] = ownedMemAP{}
		c.memories = c.memories[:last]
	}
	return nil
}
