package swd

import "context"

// LineReset emits 56 high cycles followed by eight idle cycles. It invalidates
// an established connection; call Connect before another register operation.
func (c *Conn) LineReset(ctx context.Context) error {
	if c != nil {
		c.requireRepair()
	}
	seq := &sequence{}
	seq.appendN(56, true, true)
	seq.appendN(8, true, false)
	_, err := c.exchange(ctx, seq)
	return err
}

// JTAGToSWD selects SWD with line resets around the standard sequence. It
// invalidates an established connection; call Connect before another register
// operation.
func (c *Conn) JTAGToSWD(ctx context.Context) error {
	if c != nil {
		c.requireRepair()
	}
	return c.enterSWD(ctx)
}

func (c *Conn) enterSWD(ctx context.Context) error {
	seq := &sequence{}
	seq.appendN(56, true, true)
	seq.appendByte(true, 0x9e)
	seq.appendByte(true, 0xe7)
	seq.appendN(56, true, true)
	seq.appendN(8, true, false)
	_, err := c.exchange(ctx, seq)
	return err
}
