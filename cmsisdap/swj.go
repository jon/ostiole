package cmsisdap

import (
	"context"
	"errors"
	"fmt"
)

// SWJSequence drives 1 through 256 SWDIO/TMS bits using DAP_SWJ_Sequence.
// Bits are packed least-significant first. The complete sequence must fit one
// command; invalid input sends no traffic. This mandatory command does not
// require optional DAP_SWD_Sequence support. Configure SWD before calling it.
// A started command finishes under the session completion budget before caller
// cancellation is returned. Do not send sequences beneath a live protocol owner.
func (s *Session) SWJSequence(ctx context.Context, output []byte, bits int) error {
	if bits <= 0 || bits > 256 {
		return fmt.Errorf("cmsisdap: invalid %d-bit SWJ sequence", bits)
	}
	direction := make([]byte, (bits+7)/8)
	for i := range direction {
		direction[i] = 0xff
	}
	_, err := (swjOutput{session: s}).SWDIO(ctx, direction, output, bits)
	return err
}

// swjOutput lets SWD's output-only entry operations use the same command path.
// It rejects capture phases before traffic and cannot execute register frames.
type swjOutput struct{ session *Session }

func (w swjOutput) SWDIO(ctx context.Context, direction, output []byte, bits int) (input []byte, err error) {
	if bits <= 0 || bits > 256 || len(direction) < (bits+7)/8 || len(output) < (bits+7)/8 {
		return nil, fmt.Errorf("cmsisdap: invalid %d-bit SWJ sequence", bits)
	}
	for bit := range bits {
		if !swdBit(direction, bit) {
			return nil, errors.New("cmsisdap: SWJ sequence cannot sample SWDIO")
		}
	}
	if err := w.session.transportReady(ctx); err != nil {
		return nil, err
	}
	if !w.session.configured {
		return nil, errors.New("cmsisdap: SWD is not configured")
	}
	if 2+(bits+7)/8 > w.session.packetSize {
		return nil, errors.New("cmsisdap: SWJ sequence exceeds packet size")
	}
	defer func() { err = errors.Join(err, ctx.Err()) }()
	request := append([]byte{0x12, byte(bits)}, packSWDBits(output, 0, bits)...)
	response, err := w.session.exchange(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := w.session.validateStatusResponse(response, 0x12); err != nil {
		return nil, err
	}
	return make([]byte, (bits+7)/8), nil
}
