//go:build darwin && cgo

package usb

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include "iokit_darwin.h"
*/
import "C"

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
	iface        *iokitInterface
	routes       map[uint8]darwinPipe
	submissions  chan iokitBulkSubmission
	aborts       chan iokitBulkAbort
	closes       chan chan error
	closed       chan struct{}
	mu           sync.Mutex
	pendingCount int
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
	native   *C.ostiole_usb_bulk_transfer
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
	return opener.openBulkTransfers(d.routes)
}

func (i *iokitInterface) openBulkTransfers(routes map[uint8]darwinPipe) (bulkTransferEngine, error) {
	engine := &iokitBulkEngine{
		iface: i, routes: routes, submissions: make(chan iokitBulkSubmission),
		aborts: make(chan iokitBulkAbort), closes: make(chan chan error),
		closed: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go engine.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return engine, nil
}

func (e *iokitBulkEngine) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e.iface.transfer.Lock()
	defer e.iface.transfer.Unlock()
	var result C.kern_return_t
	native := C.ostiole_usb_bulk_engine_open(e.iface.native, &result)
	if native == nil {
		ready <- iokitError(result)
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
			C.ostiole_usb_bulk_engine_poll(native, 1)
		}
	}
}

func (e *iokitBulkEngine) submitTransfer(native *C.ostiole_usb_bulk_engine, transfers map[*iokitBulkTransfer]struct{}, submission iokitBulkSubmission) {
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
	var result C.kern_return_t
	input := C.uint8_t(0)
	if submission.endpoint&0x80 != 0 {
		input = 1
	}
	transfer := &iokitBulkTransfer{engine: e, endpoint: submission.endpoint, buffer: submission.buffer, done: make(chan struct{})}
	transfer.native = C.ostiole_usb_bulk_transfer_submit(native, C.uint8_t(pipe.ref), input,
		bytePointer(submission.buffer), C.uint32_t(len(submission.buffer)), &result)
	runtime.KeepAlive(submission.buffer)
	if transfer.native == nil {
		submission.reply <- iokitBulkSubmissionResult{err: fmt.Errorf("usb: submit bulk transfer: %w", iokitError(result))}
		return
	}
	transfers[transfer] = struct{}{}
	e.addPending(1)
	submission.reply <- iokitBulkSubmissionResult{transfer: transfer}
}

func (e *iokitBulkEngine) takeCompletions(transfers map[*iokitBulkTransfer]struct{}) bool {
	taken := false
	for transfer := range transfers {
		event := C.ostiole_usb_bulk_transfer_take(transfer.native, bytePointer(transfer.buffer), C.uint32_t(len(transfer.buffer)))
		runtime.KeepAlive(transfer.buffer)
		if event.available == 0 {
			continue
		}
		count, err := darwinBulkCompletion(uint32(event.transfer.done), iokitTransferError(uint32(event.transfer.result), uint32(event.transfer.cleanup)))
		C.ostiole_usb_bulk_transfer_free(transfer.native)
		transfer.native = nil
		delete(transfers, transfer)
		transfer.complete(int(count), err)
		taken = true
	}
	return taken
}

func (e *iokitBulkEngine) abortEndpoint(native *C.ostiole_usb_bulk_engine, transfers map[*iokitBulkTransfer]struct{}, endpoint uint8) error {
	if endpointPendingDarwin(transfers, endpoint) == 0 {
		return nil
	}
	pipe, ok := e.routes[endpoint]
	if !ok {
		return fmt.Errorf("usb: endpoint %#02x is not active", endpoint)
	}
	return abortAndDrainDarwin(func() error {
		result := C.ostiole_usb_bulk_engine_abort(native, C.uint8_t(pipe.ref))
		if result != C.kIOReturnSuccess {
			return fmt.Errorf("usb: abort endpoint %#02x: %w", endpoint, iokitError(result))
		}
		return nil
	}, func() error {
		err := waitForDarwinDrain(func() bool {
			return endpointPendingDarwin(transfers, endpoint) != 0
		}, func() {
			C.ostiole_usb_bulk_engine_poll(native, 1)
			e.takeCompletions(transfers)
		}, time.Now().Add(bulkCleanupTimeout))
		if err != nil {
			return fmt.Errorf("usb: drain endpoint %#02x: %w", endpoint, err)
		}
		return nil
	})
}

func abortAndDrainDarwin(abort func() error, drain func() error) error {
	if err := abort(); err != nil {
		return err
	}
	return drain()
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

func (e *iokitBulkEngine) closeTransfers(native *C.ostiole_usb_bulk_engine, transfers map[*iokitBulkTransfer]struct{}) error {
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
	C.ostiole_usb_bulk_engine_close(native)
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
