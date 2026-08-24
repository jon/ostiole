package jlink

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// ErrSessionPoisoned reports that an earlier ambiguous USB transfer left the
// application session unsafe to reuse. Close the session and open a new one.
var ErrSessionPoisoned = errors.New("jlink: session is poisoned")

func (s *Session) exchange(ctx context.Context, request []byte, responseSize int) ([]byte, error) {
	if err := s.writeExact(ctx, request); err != nil {
		return nil, err
	}
	return s.readResponse(ctx, responseSize)
}

func (s *Session) writeExact(ctx context.Context, data []byte) error {
	if err := s.transportReady(ctx); err != nil {
		return err
	}
	for position := 0; position < len(data); {
		count, err := s.bulkTransfer(ctx, s.application.bulkOut.Address, data[position:])
		if count < 0 || count > len(data)-position {
			return s.poison(fmt.Errorf("jlink: invalid bulk-write count %d for %d bytes", count, len(data)-position))
		}
		if err != nil {
			return s.poison(err)
		}
		if count == 0 {
			return s.poison(io.ErrNoProgress)
		}
		position += count
	}
	return nil
}

func (s *Session) readExact(ctx context.Context, size int) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("jlink: negative response size %d", size)
	}
	if err := s.transportReady(ctx); err != nil {
		return nil, err
	}
	response := make([]byte, size)
	zeroLength := false
	for position := 0; position < size; {
		if len(s.input) != 0 {
			count := copy(response[position:], s.input)
			s.input = s.input[count:]
			position += count
			continue
		}
		input, err := s.readBulk(ctx)
		if err != nil {
			return nil, err
		}
		if len(input) == 0 {
			if zeroLength {
				return nil, io.ErrNoProgress
			}
			zeroLength = true
			continue
		}
		zeroLength = false
		s.input = input
	}
	return response, nil
}

func (s *Session) readResponse(ctx context.Context, size int) ([]byte, error) {
	if size == 0 {
		return []byte{}, nil
	}
	response, err := s.readExact(ctx, size)
	if err == nil || errors.Is(err, ErrSessionPoisoned) {
		return response, err
	}
	return nil, s.poison(err)
}

func (s *Session) transportReady(ctx context.Context) error {
	if s == nil || s.device == nil {
		return errors.New("jlink: session is closed")
	}
	if s.claim == nil {
		return errors.New("jlink: application interface is closed")
	}
	if s.poisoned {
		return ErrSessionPoisoned
	}
	if ctx == nil {
		return errors.New("jlink: nil transfer context")
	}
	return ctx.Err()
}

func (s *Session) readBulk(ctx context.Context) ([]byte, error) {
	buffer := make([]byte, int(s.application.bulkIn.MaxPacketSize))
	count, err := s.bulkTransfer(ctx, s.application.bulkIn.Address, buffer)
	if err != nil {
		return nil, err
	}
	if count < 0 || count > len(buffer) {
		return nil, fmt.Errorf("jlink: invalid bulk-read count %d for %d bytes", count, len(buffer))
	}
	return buffer[:count], nil
}

func (s *Session) bulkTransfer(ctx context.Context, endpoint uint8, buffer []byte) (int, error) {
	transfer, err := s.claim.SubmitBulk(ctx, endpoint, buffer)
	if err != nil {
		return 0, err
	}
	count, err := transfer.Wait(ctx)
	if err == nil || ctx.Err() == nil {
		return count, err
	}
	transferErr := err
	if !errors.Is(transferErr, ctx.Err()) {
		transferErr = errors.Join(transferErr, ctx.Err())
	}
	abortErr := s.claim.AbortBulk(endpoint)
	if abortErr != nil {
		return 0, errors.Join(transferErr, abortErr)
	}
	_, completionErr := transfer.Wait(context.Background())
	return 0, errors.Join(transferErr, completionErr)
}

func (s *Session) poison(err error) error {
	if err == nil {
		return nil
	}
	s.poisoned = true
	return errors.Join(err, ErrSessionPoisoned)
}
