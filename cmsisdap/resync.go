package cmsisdap

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	inputQuietInterval = 100 * time.Millisecond
	inputSyncTimeout   = time.Second
)

func (s *Session) synchronizeInput(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, inputSyncTimeout)
	defer cancel()
	for {
		quiet, err := s.waitForInputQuiet(bounded)
		err = errors.Join(err, bounded.Err())
		if err != nil {
			return fmt.Errorf("cmsisdap: resynchronize command IN: %w", s.submissionError(err))
		}
		if quiet {
			return nil
		}
	}
}

func (s *Session) waitForInputQuiet(ctx context.Context) (bool, error) {
	quiet, cancel := context.WithTimeout(ctx, inputQuietInterval)
	defer cancel()
	buffer := make([]byte, s.packetSize)
	transfer, err := s.claim.SubmitBulk(quiet, s.command.bulkIn.Address, buffer)
	if err != nil {
		return false, err
	}
	count, err := transfer.Wait(quiet)
	if err == nil {
		if count < 0 || count > len(buffer) {
			return false, fmt.Errorf("invalid stale response count %d for %d bytes", count, len(buffer))
		}
		return false, nil
	}
	if abortErr := s.claim.AbortBulk(s.command.bulkIn.Address); abortErr != nil {
		return false, errors.Join(err, abortErr)
	}
	expired := err == context.DeadlineExceeded && quiet.Err() == context.DeadlineExceeded && ctx.Err() == nil
	return finishQuietRead(transfer, len(buffer), err, expired)
}

func finishQuietRead(transfer usbBulkTransfer, capacity int, waitErr error, expired bool) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), inputQuietInterval)
	defer cancel()
	count, err := transfer.Wait(ctx)
	if err == nil {
		if count < 0 || count > capacity {
			return false, errors.Join(waitErr, fmt.Errorf("invalid drained response count %d for %d bytes", count, capacity))
		}
		if expired {
			return false, nil
		}
		return false, waitErr
	}
	if expired && transfer.Aborted() {
		return true, nil
	}
	return false, errors.Join(waitErr, err)
}
