package cmsisdap

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// ErrSessionPoisoned reports that an ambiguous USB transfer left the command
// session unsafe to reuse. Close the session and open a new one.
var ErrSessionPoisoned = errors.New("cmsisdap: session is poisoned")

func (s *Session) exchange(ctx context.Context, request []byte) ([]byte, error) {
	if err := s.transportReady(ctx); err != nil {
		return nil, err
	}
	if len(request) == 0 {
		return nil, errors.New("cmsisdap: empty command")
	}
	if len(request) > s.packetSize {
		return nil, fmt.Errorf("cmsisdap: command is %d bytes, packet size is %d", len(request), s.packetSize)
	}
	response := make([]byte, s.packetSize)
	input, err := s.claim.SubmitBulk(ctx, s.command.bulkIn.Address, response)
	if err != nil {
		return nil, fmt.Errorf("cmsisdap: submit response IN: %w", err)
	}
	output, err := s.claim.SubmitBulk(ctx, s.command.bulkOut.Address, request)
	if err != nil {
		cleanupErr := s.claim.AbortBulk(s.command.bulkIn.Address)
		if cleanupErr != nil {
			return nil, s.poison(errors.Join(fmt.Errorf("cmsisdap: submit command OUT: %w", err), cleanupErr))
		}
		return nil, fmt.Errorf("cmsisdap: submit command OUT: %w", err)
	}
	if err := s.completeCommand(ctx, output, len(request)); err != nil {
		return nil, err
	}
	return s.completeResponse(ctx, input, response)
}

func (s *Session) completeCommand(ctx context.Context, transfer usbBulkTransfer, size int) error {
	count, err := transfer.Wait(ctx)
	if err != nil {
		cleanupErr := errors.Join(s.claim.AbortBulk(s.command.bulkOut.Address), s.claim.AbortBulk(s.command.bulkIn.Address))
		return s.poison(errors.Join(fmt.Errorf("cmsisdap: complete command OUT: %w", err), cleanupErr))
	}
	if count != size {
		cleanupErr := s.claim.AbortBulk(s.command.bulkIn.Address)
		return s.poison(errors.Join(fmt.Errorf("cmsisdap: command OUT completed %d of %d bytes", count, size), cleanupErr))
	}
	return nil
}

func (s *Session) completeResponse(ctx context.Context, transfer usbBulkTransfer, response []byte) ([]byte, error) {
	count, err := transfer.Wait(ctx)
	if err != nil {
		cleanupErr := s.claim.AbortBulk(s.command.bulkIn.Address)
		return nil, s.poison(errors.Join(fmt.Errorf("cmsisdap: complete response IN: %w", err), cleanupErr))
	}
	if count < 0 || count > len(response) {
		return nil, s.poison(fmt.Errorf("cmsisdap: response IN completed with invalid count %d for %d-byte buffer", count, len(response)))
	}
	if count == 0 {
		return nil, s.poison(fmt.Errorf("cmsisdap: response IN made no progress: %w", io.ErrNoProgress))
	}
	return response[:count], nil
}

func (s *Session) transportReady(ctx context.Context) error {
	if s == nil || s.device == nil {
		return errors.New("cmsisdap: session is closed")
	}
	if s.claim == nil {
		return errors.New("cmsisdap: command interface is closed")
	}
	if s.poisoned {
		return ErrSessionPoisoned
	}
	if ctx == nil {
		return errors.New("cmsisdap: nil transfer context")
	}
	return ctx.Err()
}

func (s *Session) poison(err error) error {
	if err == nil {
		return nil
	}
	s.poisoned = true
	return errors.Join(err, ErrSessionPoisoned)
}
