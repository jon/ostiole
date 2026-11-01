//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type testIOKitNative struct {
	mu        sync.Mutex
	requests  []*testIOKitRequest
	aborted   []uint8
	closes    int
	submitErr error
	abortErr  error
	withhold  bool
}

type testIOKitRequest struct {
	owner *testIOKitNative
	pipe  uint8
	input bool
	size  int
	event darwinBulkEvent
	ready bool
	freed int
}

func (n *testIOKitNative) submit(pipe uint8, input bool, buffer []byte) (darwinBulkRequest, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.submitErr != nil {
		return nil, n.submitErr
	}
	r := &testIOKitRequest{owner: n, pipe: pipe, input: input, size: len(buffer)}
	n.requests = append(n.requests, r)
	return r, nil
}

func (n *testIOKitNative) poll()  { time.Sleep(100 * time.Microsecond) }
func (n *testIOKitNative) close() { n.mu.Lock(); n.closes++; n.mu.Unlock() }
func (n *testIOKitNative) abort(pipe uint8) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.aborted = append(n.aborted, pipe)
	if n.abortErr != nil {
		return n.abortErr
	}
	if !n.withhold {
		for _, r := range n.requests {
			if r.pipe == pipe && r.freed == 0 {
				r.ready = true
				r.event = darwinBulkEvent{available: true, err: context.Canceled}
			}
		}
	}
	return nil
}
func (r *testIOKitRequest) take([]byte) darwinBulkEvent {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if !r.ready {
		return darwinBulkEvent{}
	}
	r.ready = false
	return r.event
}
func (r *testIOKitRequest) free() { r.owner.mu.Lock(); r.freed++; r.owner.mu.Unlock() }

func startIOKitEngine(t *testing.T, n *testIOKitNative) *iokitBulkEngine {
	t.Helper()
	e := newIOKitBulkEngine(&iokitInterface{}, map[uint8]darwinPipe{0x81: {ref: 7}, 0x02: {ref: 4}})
	e.openNative = func() (darwinBulkNative, error) { return n, nil }
	e.cleanupTimeout = -time.Second
	ready := make(chan error, 1)
	go e.run(ready)
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		n.mu.Lock()
		n.abortErr = nil
		n.withhold = false
		n.mu.Unlock()
		e.cleanupTimeout = time.Second
		if err := e.close(); err != nil {
			t.Errorf("engine cleanup: %v", err)
		}
	})
	return e
}

func submitIOKit(t *testing.T, e *iokitBulkEngine, endpoint uint8) *BulkTransfer {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	transfer, err := e.submit(ctx, endpoint, make([]byte, 8))
	if err != nil {
		t.Fatal(err)
	}
	return &BulkTransfer{backend: transfer}
}

func TestIOKitEngineCompletesAndFreesNativeRequests(t *testing.T) {
	for _, test := range []struct {
		name  string
		count uint32
		err   error
	}{
		{"short", 3, nil}, {"zero", 0, nil}, {"failure", 5, errors.New("native completion failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := &testIOKitNative{}
			e := startIOKitEngine(t, n)
			transfer := submitIOKit(t, e, 0x81)
			if !e.pending() {
				t.Fatal("accepted request was not pending")
			}
			n.mu.Lock()
			r := n.requests[0]
			if r.pipe != 7 || !r.input || r.size != 8 {
				n.mu.Unlock()
				t.Fatalf("native IN submission pipe=%d input=%t size=%d", r.pipe, r.input, r.size)
			}
			r.event = darwinBulkEvent{available: true, count: test.count, err: test.err}
			r.ready = true
			n.mu.Unlock()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			count, err := transfer.Wait(ctx)
			want := int(test.count)
			if test.err != nil {
				want = 0
			}
			if count != want || !errors.Is(err, test.err) {
				t.Fatalf("Wait() = %d, %v; want %d, %v", count, err, want, test.err)
			}
			n.mu.Lock()
			freed := r.freed
			n.mu.Unlock()
			if freed != 1 || e.pending() {
				t.Fatalf("freed=%d pending=%t", freed, e.pending())
			}
		})
	}
}

func TestIOKitEngineRejectedSubmissionOwnsNoRequest(t *testing.T) {
	n := &testIOKitNative{submitErr: errors.New("native submit failed")}
	e := startIOKitEngine(t, n)
	if _, err := e.submit(t.Context(), 0x81, make([]byte, 8)); !errors.Is(err, n.submitErr) {
		t.Fatalf("submit() = %v", err)
	}
	if e.pending() || len(n.requests) != 0 {
		t.Fatal("rejected submission acquired a request")
	}
}

func TestIOKitEngineAbortFailureRetainsRequestsForRetry(t *testing.T) {
	n := &testIOKitNative{abortErr: errors.New("native abort failed")}
	e := startIOKitEngine(t, n)
	transfer := submitIOKit(t, e, 0x81)
	if err := e.close(); !errors.Is(err, n.abortErr) {
		t.Fatalf("close() = %v", err)
	}
	if !e.pending() || channelClosed(transfer.Done()) || channelClosed(e.closed) {
		t.Fatal("failed abort released ownership")
	}
	n.mu.Lock()
	n.abortErr = nil
	n.mu.Unlock()
	e.cleanupTimeout = time.Second
	if err := e.close(); err != nil {
		t.Fatal(err)
	}
	if e.pending() || !channelClosed(transfer.Done()) {
		t.Fatal("close retry did not drain request")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.aborted) != 2 || n.aborted[0] != 7 || n.aborted[1] != 7 || n.requests[0].freed != 1 || n.closes != 1 {
		t.Fatalf("aborts=%v freed=%d closes=%d", n.aborted, n.requests[0].freed, n.closes)
	}
}

func TestIOKitEngineDrainTimeoutRetainsRequestsForRetry(t *testing.T) {
	n := &testIOKitNative{withhold: true}
	e := startIOKitEngine(t, n)
	in := submitIOKit(t, e, 0x81)
	out := submitIOKit(t, e, 0x02)
	if err := e.abort(0x81); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("abort() = %v, want deadline", err)
	}
	if !e.pending() || channelClosed(in.Done()) || channelClosed(out.Done()) {
		t.Fatal("timed-out drain released a pending request")
	}
	n.mu.Lock()
	n.withhold = false
	n.mu.Unlock()
	e.cleanupTimeout = time.Second
	if err := e.abort(0x81); err != nil {
		t.Fatal(err)
	}
	if !e.pending() || !channelClosed(in.Done()) || channelClosed(out.Done()) {
		t.Fatal("endpoint drain affected the wrong request")
	}
	if err := e.close(); err != nil {
		t.Fatal(err)
	}
	if e.pending() || !channelClosed(out.Done()) {
		t.Fatal("final close did not drain OUT request")
	}
}

func TestIOKitEngineRejectsOversizedCompletion(t *testing.T) {
	for _, endpoint := range []uint8{0x81, 0x02} {
		n := &testIOKitNative{}
		e := startIOKitEngine(t, n)
		transfer := submitIOKit(t, e, endpoint)
		n.mu.Lock()
		r := n.requests[0]
		r.event = darwinBulkEvent{available: true, count: 9}
		r.ready = true
		n.mu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		count, err := transfer.Wait(ctx)
		cancel()
		if count != 0 || err == nil {
			t.Fatalf("Wait() = %d, %v, want invalid completion", count, err)
		}
		if e.pending() {
			t.Fatal("invalid completion retained a native request")
		}
	}
}

func TestIOKitEnginePreflightSendsNoNativeRequest(t *testing.T) {
	n := &testIOKitNative{}
	e := startIOKitEngine(t, n)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.submit(ctx, 0x81, make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("submit() = %v, want cancellation", err)
	}
	if _, err := e.submit(t.Context(), 0x83, make([]byte, 8)); err == nil {
		t.Fatal("inactive endpoint submitted")
	}
	n.mu.Lock()
	count := len(n.requests)
	n.mu.Unlock()
	if count != 0 || e.pending() {
		t.Fatal("invalid request reached native submission")
	}
}

func TestIOKitEngineFailedOpenAcquiresNoRequests(t *testing.T) {
	want := errors.New("native engine open failed")
	e := newIOKitBulkEngine(&iokitInterface{}, nil)
	e.openNative = func() (darwinBulkNative, error) { return nil, want }
	ready := make(chan error, 1)
	go e.run(ready)
	if err := <-ready; !errors.Is(err, want) {
		t.Fatalf("engine open = %v", err)
	}
	<-e.closed
	if e.pending() {
		t.Fatal("failed engine open acquired requests")
	}
	if err := e.close(); err != nil {
		t.Fatal(err)
	}
}

func TestIOKitSuccessIsNotError(t *testing.T) {
	if err := iokitErrorCode(0); err != nil {
		t.Fatalf("successful native operation returned %v", err)
	}
	if err := iokitErrorCode(1); err == nil {
		t.Fatal("failed native operation returned nil")
	}
}
