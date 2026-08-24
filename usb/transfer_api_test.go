package usb

import (
	"context"
	"errors"
	"testing"
)

type transferControlReply struct {
	requestType uint8
	request     uint8
	value       uint16
	index       uint16
	data        []byte
}

type scriptedTransferDevice struct {
	t       *testing.T
	replies []transferControlReply
	calls   int
}

func (d *scriptedTransferDevice) ControlTransfer(_ context.Context, requestType, request uint8, value, index uint16, data []byte) (int, error) {
	d.t.Helper()
	if d.calls >= len(d.replies) {
		d.t.Fatalf("unexpected control transfer type=%#02x request=%#02x value=%#04x index=%d", requestType, request, value, index)
	}
	reply := d.replies[d.calls]
	d.calls++
	if requestType != reply.requestType || request != reply.request || value != reply.value || index != reply.index {
		d.t.Fatalf("control transfer %d = %#02x/%#02x/%#04x/%d, want %#02x/%#02x/%#04x/%d", d.calls, requestType, request, value, index, reply.requestType, reply.request, reply.value, reply.index)
	}
	copy(data, reply.data)
	return len(reply.data), nil
}

type fakeBulkTransferBackend struct {
	done       chan struct{}
	failed     chan struct{}
	count      int
	err        error
	failureErr error
}

func (b *fakeBulkTransferBackend) completion() <-chan struct{} { return b.done }

func (b *fakeBulkTransferBackend) result() (int, error) { return b.count, b.err }

func (b *fakeBulkTransferBackend) failure() <-chan struct{} { return b.failed }

func (b *fakeBulkTransferBackend) failureResult() error { return b.failureErr }

func TestBulkTransferWaitDoesNotCancelOnContextEnd(t *testing.T) {
	backend := &fakeBulkTransferBackend{done: make(chan struct{})}
	transfer := &BulkTransfer{backend: backend}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transfer.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v", err)
	}
	backend.count = 7
	close(backend.done)
	if count, err := transfer.Wait(context.Background()); err != nil || count != 7 {
		t.Fatalf("second Wait() = %d, %v", count, err)
	}
}

func TestBulkTransferReturnsOnlySuccessfulCounts(t *testing.T) {
	want := errors.New("transfer failed")
	done := make(chan struct{})
	close(done)
	transfer := &BulkTransfer{backend: &fakeBulkTransferBackend{done: done, count: 14, err: want}}
	if count, err := transfer.Wait(context.Background()); count != 0 || !errors.Is(err, want) {
		t.Fatalf("Wait() = %d, %v", count, err)
	}
}

func TestBulkTransferReportsEngineFailureWithoutReleasingBuffer(t *testing.T) {
	want := errors.New("engine failed")
	done := make(chan struct{})
	failed := make(chan struct{})
	close(failed)
	transfer := &BulkTransfer{backend: &fakeBulkTransferBackend{done: done, failed: failed, failureErr: want}}
	if count, err := transfer.Wait(context.Background()); count != 0 || !errors.Is(err, want) {
		t.Fatalf("Wait() = %d, %v", count, err)
	}
	select {
	case <-transfer.Done():
		t.Fatal("engine failure released the transfer buffer")
	default:
	}
}

func TestClaimedInterfaceEndpointsFollowCurrentAlternate(t *testing.T) {
	raw := rawConfiguration(7, 1,
		interfaceDescriptor(0, 0, 1), endpointDescriptor(0x81),
		interfaceDescriptor(0, 1, 1), endpointDescriptor(0x82),
	)
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	device := &scriptedTransferDevice{t: t, replies: []transferControlReply{
		{requestType: 0x81, request: 0x0a, index: 0, data: []byte{1}},
		{requestType: 0x80, request: 0x08, data: []byte{7}},
		{requestType: 0x80, request: 0x06, value: 0x0100, data: deviceDescriptor},
		{requestType: 0x80, request: 0x06, value: 0x0200, data: raw[:9]},
		{requestType: 0x80, request: 0x06, value: 0x0200, data: raw},
		{requestType: 0x80, request: 0x08, data: []byte{7}},
	}}

	alternate, endpoints, err := claimedInterfaceEndpoints(context.Background(), device, 0)
	if err != nil {
		t.Fatal(err)
	}
	if alternate != 1 || len(endpoints) != 1 || endpoints[0x82].Address != 0x82 {
		t.Fatalf("alternate endpoints = %d/%#v", alternate, endpoints)
	}
	if _, ok := endpoints[0x81]; ok {
		t.Fatalf("alternate zero endpoint remained active: %#v", endpoints[0x81])
	}
}
