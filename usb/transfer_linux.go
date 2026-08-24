//go:build linux

package usb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	usbfsURBBulk    = 3
	usbfsDiscardURB = uintptr(0x550b)
)

const (
	usbfsSubmitURB      = uintptr(0x8000550a) | unsafe.Sizeof(usbURB{})<<16
	usbfsReapURBNoDelay = uintptr(0x4000550d) | unsafe.Sizeof(uintptr(0))<<16
)

type usbURB struct {
	Type            uint8
	Endpoint        uint8
	Status          int32
	Flags           uint32
	Buffer          unsafe.Pointer
	BufferLength    int32
	ActualLength    int32
	StartFrame      int32
	NumberOfPackets int32
	ErrorCount      int32
	SignalNumber    uint32
	UserContext     uintptr
}

type linuxBulkEngine struct {
	device         *Device
	poller         linuxBulkPoller
	submissions    chan linuxBulkSubmission
	aborts         chan linuxBulkAbort
	closes         chan chan error
	completions    chan error
	watchStop      chan struct{}
	watchDone      chan struct{}
	closed         chan struct{}
	failed         chan struct{}
	cleanupTimeout time.Duration
	watchOnce      sync.Once
	watchErr       error
	mu             sync.Mutex
	pendingCount   int
	failureErr     error
}

type linuxBulkSubmission struct {
	ctx      context.Context
	endpoint uint8
	buffer   []byte
	reply    chan linuxBulkSubmissionResult
}

type linuxBulkSubmissionResult struct {
	transfer *linuxBulkTransfer
	err      error
}

type linuxBulkAbort struct {
	endpoint uint8
	reply    chan error
}

type linuxBulkTransfer struct {
	engine   *linuxBulkEngine
	endpoint uint8
	buffer   []byte
	data     []byte
	urb      usbURB
	pinner   runtime.Pinner
	done     chan struct{}
	mu       sync.Mutex
	count    int
	err      error
	pending  bool
}

func (d *Device) openBulkTransfers(claim *ClaimedInterface) (bulkTransferEngine, error) {
	if d.claim != claim {
		return nil, errors.New("usb: claimed interface is not owned by this device")
	}
	newPoller := d.newBulkPoller
	if newPoller == nil {
		newPoller = newUSBFSBulkPoller
	}
	poller, err := newPoller(d.file.Fd())
	if err != nil {
		return nil, err
	}
	engine := &linuxBulkEngine{
		device: d, poller: poller, submissions: make(chan linuxBulkSubmission),
		aborts: make(chan linuxBulkAbort), closes: make(chan chan error),
		completions: make(chan error), watchStop: make(chan struct{}),
		watchDone: make(chan struct{}), closed: make(chan struct{}),
		failed: make(chan struct{}), cleanupTimeout: bulkCleanupTimeout,
	}
	ready := make(chan struct{})
	go engine.run(ready)
	<-ready
	return engine, nil
}

func (e *linuxBulkEngine) run(ready chan<- struct{}) {
	e.device.bulk.Lock()
	defer e.device.bulk.Unlock()
	transfers := make(map[*usbURB]*linuxBulkTransfer)
	go e.watchCompletions()
	close(ready)
	for {
		select {
		case waitErr := <-e.completions:
			reapErr := e.reapAvailable(transfers)
			var err error
			if waitErr != nil {
				err = fmt.Errorf("usb: wait for bulk completion: %w", waitErr)
			}
			if reapErr != nil {
				err = errors.Join(err, reapErr)
			}
			if err != nil {
				e.recordFailure(err)
				_ = e.stopWatcher()
			}
		case submission := <-e.submissions:
			e.submitTransfer(transfers, submission)
		case abort := <-e.aborts:
			abort.reply <- e.abortEndpoint(transfers, abort.endpoint)
		case reply := <-e.closes:
			err := e.closeTransfers(transfers)
			if len(transfers) != 0 {
				reply <- err
				continue
			}
			err = errors.Join(err, e.stopWatcher())
			reply <- err
			close(e.closed)
			return
		}
	}
}

func (e *linuxBulkEngine) watchCompletions() {
	defer close(e.watchDone)
	for {
		err := e.poller.wait()
		if errors.Is(err, errLinuxBulkPollerStopped) {
			return
		}
		select {
		case e.completions <- err:
		case <-e.watchStop:
			return
		}
	}
}

func (e *linuxBulkEngine) stopWatcher() error {
	e.watchOnce.Do(func() {
		close(e.watchStop)
		e.watchErr = e.poller.stopWaiting()
		<-e.watchDone
		e.watchErr = errors.Join(e.watchErr, e.poller.close())
	})
	return e.watchErr
}

func (e *linuxBulkEngine) submitTransfer(transfers map[*usbURB]*linuxBulkTransfer, submission linuxBulkSubmission) {
	if err := e.failure(); err != nil {
		submission.reply <- linuxBulkSubmissionResult{err: err}
		return
	}
	if err := submission.ctx.Err(); err != nil {
		submission.reply <- linuxBulkSubmissionResult{err: err}
		return
	}
	if len(submission.buffer) > math.MaxInt32 {
		submission.reply <- linuxBulkSubmissionResult{err: errors.New("usb: bulk transfer exceeds usbfs URB limit")}
		return
	}
	transfer := &linuxBulkTransfer{
		engine: e, endpoint: submission.endpoint, buffer: submission.buffer,
		data: make([]byte, len(submission.buffer)), done: make(chan struct{}),
	}
	if submission.endpoint&0x80 == 0 {
		copy(transfer.data, submission.buffer)
	}
	transfer.urb = newBulkURB(submission.endpoint, transfer.data)
	pinBulkURB(&transfer.pinner, &transfer.urb, transfer.data)
	if _, err := e.device.runIOCTL(usbfsSubmitURB, &transfer.urb); err != nil {
		transfer.pinner.Unpin()
		submission.reply <- linuxBulkSubmissionResult{err: fmt.Errorf("usb: submit bulk transfer: %w", err)}
		return
	}
	transfer.pending = true
	transfers[&transfer.urb] = transfer
	e.addPending(1)
	submission.reply <- linuxBulkSubmissionResult{transfer: transfer}
}

func (e *linuxBulkEngine) reapAvailable(transfers map[*usbURB]*linuxBulkTransfer) error {
	for {
		completed, err := e.reap(transfers)
		if err != nil || !completed {
			return err
		}
	}
}

func (e *linuxBulkEngine) reap(transfers map[*usbURB]*linuxBulkTransfer) (bool, error) {
	var completed *usbURB
	_, err := e.device.runIOCTL(usbfsReapURBNoDelay, &completed)
	if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("usb: reap bulk transfer: %w", err)
	}
	transfer := transfers[completed]
	if transfer == nil {
		return false, errors.New("usb: reaped an unknown bulk transfer")
	}
	delete(transfers, completed)
	transfer.complete(validateBulkURB(completed))
	return true, nil
}

func (e *linuxBulkEngine) abortEndpoint(transfers map[*usbURB]*linuxBulkTransfer, endpoint uint8) error {
	var result error
	pending := 0
	for urb, transfer := range transfers {
		if transfer.endpoint != endpoint {
			continue
		}
		pending++
		if _, err := e.device.runIOCTL(usbfsDiscardURB, urb); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENODEV) {
			result = errors.Join(result, fmt.Errorf("usb: abort endpoint %#02x: %w", endpoint, err))
		}
	}
	if result != nil {
		return result
	}
	return e.drainEndpoint(transfers, endpoint, pending)
}

func (e *linuxBulkEngine) drainEndpoint(transfers map[*usbURB]*linuxBulkTransfer, endpoint uint8, pending int) error {
	deadline := time.Now().Add(e.cleanupTimeout)
	for pending != 0 {
		before := endpointPending(transfers, endpoint)
		completed, err := e.reap(transfers)
		if err != nil {
			e.recordFailure(err)
			return errors.Join(fmt.Errorf("usb: drain endpoint %#02x: %w", endpoint, err), e.stopWatcher())
		}
		pending -= before - endpointPending(transfers, endpoint)
		if completed || pending == 0 {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("usb: drain endpoint %#02x: %w", endpoint, context.DeadlineExceeded)
		}
		if e.failure() != nil {
			if remaining > 10*time.Millisecond {
				remaining = 10 * time.Millisecond
			}
			time.Sleep(remaining)
			continue
		}
		timer := time.NewTimer(remaining)
		select {
		case err := <-e.completions:
			timer.Stop()
			if err != nil {
				err = fmt.Errorf("usb: wait for bulk completion: %w", err)
				e.recordFailure(err)
				return errors.Join(fmt.Errorf("usb: drain endpoint %#02x: %w", endpoint, err), e.stopWatcher())
			}
		case <-timer.C:
			return fmt.Errorf("usb: drain endpoint %#02x: %w", endpoint, context.DeadlineExceeded)
		}
	}
	return nil
}

func endpointPending(transfers map[*usbURB]*linuxBulkTransfer, endpoint uint8) int {
	count := 0
	for _, transfer := range transfers {
		if transfer.endpoint == endpoint {
			count++
		}
	}
	return count
}

func (e *linuxBulkEngine) closeTransfers(transfers map[*usbURB]*linuxBulkTransfer) error {
	endpoints := make(map[uint8]bool)
	for _, transfer := range transfers {
		endpoints[transfer.endpoint] = true
	}
	var result error
	for endpoint := range endpoints {
		result = errors.Join(result, e.abortEndpoint(transfers, endpoint))
	}
	return result
}

func (e *linuxBulkEngine) submit(ctx context.Context, endpoint uint8, buffer []byte) (bulkTransferBackend, error) {
	reply := make(chan linuxBulkSubmissionResult, 1)
	request := linuxBulkSubmission{ctx: ctx, endpoint: endpoint, buffer: buffer, reply: reply}
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

func (e *linuxBulkEngine) abort(endpoint uint8) error {
	reply := make(chan error, 1)
	select {
	case e.aborts <- linuxBulkAbort{endpoint: endpoint, reply: reply}:
		return <-reply
	case <-e.closed:
		return nil
	}
}

func (e *linuxBulkEngine) pending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pendingCount != 0
}

func (e *linuxBulkEngine) addPending(delta int) {
	e.mu.Lock()
	e.pendingCount += delta
	e.mu.Unlock()
}

func (e *linuxBulkEngine) recordFailure(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	if e.failureErr == nil {
		e.failureErr = err
		close(e.failed)
	}
	e.mu.Unlock()
}

func (e *linuxBulkEngine) failure() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.failureErr
}

func (e *linuxBulkEngine) close() error {
	reply := make(chan error, 1)
	select {
	case e.closes <- reply:
		return <-reply
	case <-e.closed:
		return nil
	}
}

func (t *linuxBulkTransfer) completion() <-chan struct{} { return t.done }

func (t *linuxBulkTransfer) failure() <-chan struct{} { return t.engine.failed }

func (t *linuxBulkTransfer) failureResult() error { return t.engine.failure() }

func (t *linuxBulkTransfer) result() (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count, t.err
}

func (t *linuxBulkTransfer) complete(err error) {
	t.mu.Lock()
	if err == nil {
		t.count = int(t.urb.ActualLength)
		if t.endpoint&0x80 != 0 {
			copy(t.buffer, t.data[:t.count])
		}
	} else {
		t.err = err
	}
	t.pending = false
	t.mu.Unlock()
	t.pinner.Unpin()
	t.engine.addPending(-1)
	close(t.done)
}

func newBulkURB(endpoint uint8, data []byte) usbURB {
	urb := usbURB{Type: usbfsURBBulk, Endpoint: endpoint, BufferLength: int32(len(data))}
	if len(data) != 0 {
		urb.Buffer = unsafe.Pointer(&data[0])
	}
	return urb
}

func pinBulkURB(pinner *runtime.Pinner, urb *usbURB, data []byte) {
	pinner.Pin(urb)
	if len(data) != 0 {
		pinner.Pin(&data[0])
	}
}

func validateBulkURB(urb *usbURB) error {
	if urb.Status != 0 {
		return usbURBStatusError(urb)
	}
	if urb.ActualLength < 0 || urb.ActualLength > urb.BufferLength {
		return fmt.Errorf("usb: invalid bulk-transfer completion count %d for %d-byte buffer", urb.ActualLength, urb.BufferLength)
	}
	return nil
}

func usbURBStatusError(urb *usbURB) error {
	direction := "OUT"
	if urb.Endpoint&0x80 != 0 {
		direction = "IN"
	}
	return fmt.Errorf("usb: bulk %s transfer on endpoint %#02x: %w", direction, urb.Endpoint, syscall.Errno(-urb.Status))
}
