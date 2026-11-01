//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"
)

type darwinBulkTransferOpener interface {
	openBulkTransfers(map[uint8]darwinPipe) (bulkTransferEngine, error)
}

type iokitBulkEngine struct {
	iface          *iokitInterface
	openNative     func() (darwinBulkNative, error)
	cleanupTimeout time.Duration
	routes         map[uint8]darwinPipe
	submissions    chan iokitBulkSubmission
	aborts         chan iokitBulkAbort
	closes         chan chan error
	closed         chan struct{}
	mu             sync.Mutex
	pendingCount   int
}

type iokitBulkSubmission struct {
	ctx      context.Context
	endpoint uint8
	buffer   []byte
	reply    chan iokitBulkSubmissionResult
}

type iokitBulkSubmissionResult struct {
	transfer *iokitBulkTransfer
	err      error
}

type iokitBulkAbort struct {
	endpoint uint8
	reply    chan error
}

type iokitBulkTransfer struct {
	engine   *iokitBulkEngine
	endpoint uint8
	buffer   []byte
	native   darwinBulkRequest
	done     chan struct{}
	mu       sync.Mutex
	count    int
	err      error
}

func darwinBulkCompletion(count uint32, err error) (uint32, error) {
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (d *Device) openBulkTransfers(claim *ClaimedInterface) (bulkTransferEngine, error) {
	if d.claim != claim {
		return nil, errors.New("usb: claimed interface is not owned by this device")
	}
	opener, ok := d.iface.(darwinBulkTransferOpener)
	if !ok {
		return nil, errors.New("usb: interface cannot submit bulk transfers")
	}
	if d.routes == nil {
		pipes, err := d.iface.pipes()
		if err != nil {
			return nil, fmt.Errorf("usb: reload interface %d pipes: %w", claim.number, err)
		}
		d.replaceRoutes(pipes)
	}
	return opener.openBulkTransfers(d.routes)
}

func (i *iokitInterface) openBulkTransfers(routes map[uint8]darwinPipe) (bulkTransferEngine, error) {
	engine := newIOKitBulkEngine(i, routes)
	ready := make(chan error, 1)
	go engine.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return engine, nil
}

func newIOKitBulkEngine(i *iokitInterface, routes map[uint8]darwinPipe) *iokitBulkEngine {
	return &iokitBulkEngine{
		iface: i, routes: routes, openNative: i.openBulkNative,
		cleanupTimeout: bulkCleanupTimeout, submissions: make(chan iokitBulkSubmission),
		aborts: make(chan iokitBulkAbort), closes: make(chan chan error),
		closed: make(chan struct{}),
	}
}

func (e *iokitBulkEngine) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e.iface.transfer.Lock()
	defer e.iface.transfer.Unlock()
	native, err := e.openNative()
	if err != nil {
		ready <- err
		close(e.closed)
		return
	}
	transfers := make(map[*iokitBulkTransfer]struct{})
	ready <- nil
	for {
		if e.takeCompletions(transfers) {
			continue
		}
		select {
		case submission := <-e.submissions:
			e.submitTransfer(native, transfers, submission)
		case abort := <-e.aborts:
			abort.reply <- e.abortEndpoint(native, transfers, abort.endpoint)
		case reply := <-e.closes:
			err := e.closeTransfers(native, transfers)
			reply <- err
			if err != nil {
				continue
			}
			close(e.closed)
			return
		default:
			native.poll()
		}
	}
}

func (e *iokitBulkEngine) submitTransfer(native darwinBulkNative, transfers map[*iokitBulkTransfer]struct{}, submission iokitBulkSubmission) {
	if err := submission.ctx.Err(); err != nil {
		submission.reply <- iokitBulkSubmissionResult{err: err}
		return
	}
	if uint64(len(submission.buffer)) > math.MaxUint32 {
		submission.reply <- iokitBulkSubmissionResult{err: errors.New("usb: bulk transfer exceeds IOKit limit")}
		return
	}
	pipe, ok := e.routes[submission.endpoint]
	if !ok {
		submission.reply <- iokitBulkSubmissionResult{err: fmt.Errorf("usb: endpoint %#02x is not active", submission.endpoint)}
		return
	}
	transfer := &iokitBulkTransfer{engine: e, endpoint: submission.endpoint, buffer: submission.buffer, done: make(chan struct{})}
	var err error
	transfer.native, err = native.submit(pipe.ref, submission.endpoint&0x80 != 0, submission.buffer)
	if err != nil {
		submission.reply <- iokitBulkSubmissionResult{err: fmt.Errorf("usb: submit bulk transfer: %w", err)}
		return
	}
	transfers[transfer] = struct{}{}
	e.addPending(1)
	submission.reply <- iokitBulkSubmissionResult{transfer: transfer}
}

func (e *iokitBulkEngine) takeCompletions(transfers map[*iokitBulkTransfer]struct{}) bool {
	taken := false
	for transfer := range transfers {
		event := transfer.native.take(transfer.buffer)
		if !event.available {
			continue
		}
		count, err := darwinBulkCompletion(event.count, event.err)
		if uint64(count) > uint64(len(transfer.buffer)) {
			count, err = 0, errors.New("usb: native bulk completion exceeds buffer length")
		}
		transfer.native.free()
		transfer.native = nil
		delete(transfers, transfer)
		transfer.complete(int(count), err)
		taken = true
	}
	return taken
}

func (e *iokitBulkEngine) abortEndpoint(native darwinBulkNative, transfers map[*iokitBulkTransfer]struct{}, endpoint uint8) error {
	if endpointPendingDarwin(transfers, endpoint) == 0 {
		return nil
	}
	pipe, ok := e.routes[endpoint]
	if !ok {
		return fmt.Errorf("usb: endpoint %#02x is not active", endpoint)
	}
	return abortAndDrainDarwin(func() error {
		if err := native.abort(pipe.ref); err != nil {
			return fmt.Errorf("usb: abort endpoint %#02x: %w", endpoint, err)
		}
		return nil
	}, func() error {
		err := waitForDarwinDrain(func() bool {
			return endpointPendingDarwin(transfers, endpoint) != 0
		}, func() {
			native.poll()
			e.takeCompletions(transfers)
		}, time.Now().Add(e.cleanupTimeout))
		if err != nil {
			return fmt.Errorf("usb: drain endpoint %#02x: %w", endpoint, err)
		}
		return nil
	})
}

func abortAndDrainDarwin(abort func() error, drain func() error) error {
	err := abort()
	if err != nil && !errors.Is(err, errDarwinDeviceGone) {
		return err
	}
	return errors.Join(err, drain())
}

func waitForDarwinDrain(pending func() bool, poll func(), deadline time.Time) error {
	for pending() {
		if !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		poll()
	}
	return nil
}

func endpointPendingDarwin(transfers map[*iokitBulkTransfer]struct{}, endpoint uint8) int {
	count := 0
	for transfer := range transfers {
		if transfer.endpoint == endpoint {
			count++
		}
	}
	return count
}

func (e *iokitBulkEngine) closeTransfers(native darwinBulkNative, transfers map[*iokitBulkTransfer]struct{}) error {
	endpoints := make(map[uint8]bool)
	for transfer := range transfers {
		endpoints[transfer.endpoint] = true
	}
	var result error
	for endpoint := range endpoints {
		result = errors.Join(result, e.abortEndpoint(native, transfers, endpoint))
	}
	if result != nil {
		return result
	}
	native.close()
	return nil
}

func (e *iokitBulkEngine) submit(ctx context.Context, endpoint uint8, buffer []byte) (bulkTransferBackend, error) {
	reply := make(chan iokitBulkSubmissionResult, 1)
	request := iokitBulkSubmission{ctx: ctx, endpoint: endpoint, buffer: buffer, reply: reply}
	select {
	case e.submissions <- request:
	case <-e.closed:
		return nil, errors.New("usb: bulk-transfer engine is closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	result := <-reply
	return result.transfer, result.err
}

func (e *iokitBulkEngine) abort(endpoint uint8) error {
	reply := make(chan error, 1)
	select {
	case e.aborts <- iokitBulkAbort{endpoint: endpoint, reply: reply}:
		return <-reply
	case <-e.closed:
		return nil
	}
}

func (e *iokitBulkEngine) pending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pendingCount != 0
}

func (e *iokitBulkEngine) addPending(delta int) {
	e.mu.Lock()
	e.pendingCount += delta
	e.mu.Unlock()
}

func (e *iokitBulkEngine) close() error {
	reply := make(chan error, 1)
	select {
	case e.closes <- reply:
		return <-reply
	case <-e.closed:
		return nil
	}
}

func (t *iokitBulkTransfer) completion() <-chan struct{} { return t.done }

func (t *iokitBulkTransfer) failure() <-chan struct{} { return nil }

func (t *iokitBulkTransfer) failureResult() error { return nil }

func (t *iokitBulkTransfer) result() (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count, t.err
}

func (t *iokitBulkTransfer) complete(count int, err error) {
	t.mu.Lock()
	if err == nil {
		t.count = count
	} else {
		t.err = err
	}
	t.mu.Unlock()
	t.engine.addPending(-1)
	close(t.done)
}
