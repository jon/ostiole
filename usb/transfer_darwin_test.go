//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeDarwinBulkOpener struct {
	fakeDarwinInterface
	engine *fakeDarwinBulkEngine
}

type fakeDarwinEndpointDevice struct {
	fakeDarwinInterfaceDevice
	t       *testing.T
	replies []transferControlReply
	calls   int
}

func (f *fakeDarwinEndpointDevice) control(request darwinControlRequest, data []byte) (uint16, error) {
	f.t.Helper()
	if f.calls >= len(f.replies) {
		f.t.Fatalf("unexpected control transfer %#v", request)
	}
	reply := f.replies[f.calls]
	f.calls++
	if request.requestType != reply.requestType || request.request != reply.request || request.value != reply.value || request.index != reply.index {
		f.t.Fatalf("control transfer %d = %#v, want %#02x/%#02x/%#04x/%d", f.calls, request, reply.requestType, reply.request, reply.value, reply.index)
	}
	copy(data, reply.data)
	return uint16(len(reply.data)), nil
}

func (f *fakeDarwinBulkOpener) openBulkTransfers(map[uint8]darwinPipe) (bulkTransferEngine, error) {
	f.engine.opens++
	return f.engine, nil
}

type fakeDarwinBulkEngine struct {
	endpoints []uint8
	sizes     []int
	aborted   []uint8
	opens     int
	closes    int
	isPending bool
	closeErrs []error
}

func (e *fakeDarwinBulkEngine) submit(_ context.Context, endpoint uint8, buffer []byte) (bulkTransferBackend, error) {
	e.endpoints = append(e.endpoints, endpoint)
	e.sizes = append(e.sizes, len(buffer))
	done := make(chan struct{})
	close(done)
	return &fakeBulkTransferBackend{done: done, count: len(buffer)}, nil
}

func (e *fakeDarwinBulkEngine) abort(endpoint uint8) error {
	e.aborted = append(e.aborted, endpoint)
	return nil
}

func (e *fakeDarwinBulkEngine) pending() bool { return e.isPending }

func (e *fakeDarwinBulkEngine) close() error {
	e.closes++
	if len(e.closeErrs) == 0 {
		return nil
	}
	err := e.closeErrs[0]
	e.closeErrs = e.closeErrs[1:]
	return err
}

func TestDarwinBulkTransfersUseIndependentEndpointRoutes(t *testing.T) {
	engine := &fakeDarwinBulkEngine{}
	native := &fakeDarwinBulkOpener{engine: engine}
	device := &Device{iface: native, routes: map[uint8]darwinPipe{
		0x02: {ref: 4, transferType: darwinBulkPipe, maxPacketSize: 512},
		0x81: {ref: 7, transferType: darwinBulkPipe, maxPacketSize: 512},
	}}
	claim := &ClaimedInterface{device: device, endpoints: map[uint8]Endpoint{
		0x02: {Address: 0x02, TransferType: TransferBulk, MaxPacketSize: 512},
		0x81: {Address: 0x81, TransferType: TransferBulk, MaxPacketSize: 512},
	}}
	device.claim = claim
	in, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
	if err != nil {
		t.Fatal(err)
	}
	out, err := claim.SubmitBulk(context.Background(), 0x02, make([]byte, 37))
	if err != nil {
		t.Fatal(err)
	}
	assertDarwinBulkSubmissions(t, engine)
	if count, err := in.Wait(context.Background()); err != nil || count != 512 {
		t.Fatalf("IN Wait() = %d, %v", count, err)
	}
	if count, err := out.Wait(context.Background()); err != nil || count != 37 {
		t.Fatalf("OUT Wait() = %d, %v", count, err)
	}
	if err := claim.AbortBulk(0x81); err != nil {
		t.Fatal(err)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
	assertDarwinBulkCleanup(t, native)
}

func TestDarwinReclaimedInterfaceUsesItsSelectedAlternate(t *testing.T) {
	raw := rawConfiguration(7, 1,
		interfaceDescriptor(0, 0, 1), endpointDescriptor(0x81),
		interfaceDescriptor(0, 1, 1), endpointDescriptor(0x82),
	)
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	nativeInterface := &fakeDarwinInterface{pipesValue: []darwinPipe{{endpoint: 0x82, ref: 3, transferType: darwinBulkPipe, maxPacketSize: 64}}}
	native := &fakeDarwinEndpointDevice{
		fakeDarwinInterfaceDevice: fakeDarwinInterfaceDevice{iface: nativeInterface},
		t:                         t,
		replies: []transferControlReply{
			{requestType: 0x81, request: 0x0a, index: 0, data: []byte{1}},
			{requestType: 0x80, request: 0x08, data: []byte{7}},
			{requestType: 0x80, request: 0x06, value: 0x0100, data: deviceDescriptor},
			{requestType: 0x80, request: 0x06, value: 0x0200, data: raw[:9]},
			{requestType: 0x80, request: 0x06, value: 0x0200, data: raw},
			{requestType: 0x80, request: 0x08, data: []byte{7}},
		},
	}
	device := &Device{handle: native}
	claim, err := device.ClaimInterface(0)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := claim.Endpoint(context.Background(), 0x82)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Address != 0x82 || claim.alternate != 1 || !claim.altKnown {
		t.Fatalf("selected endpoint = %#v, claim = %#v", endpoint, claim)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertDarwinBulkSubmissions(t *testing.T, engine *fakeDarwinBulkEngine) {
	t.Helper()
	if engine.opens != 1 || len(engine.endpoints) != 2 || engine.endpoints[0] != 0x81 || engine.endpoints[1] != 0x02 || engine.sizes[0] != 512 || engine.sizes[1] != 37 {
		t.Fatalf("submissions = opens %d endpoints %#v sizes %#v", engine.opens, engine.endpoints, engine.sizes)
	}
}

func assertDarwinBulkCleanup(t *testing.T, native *fakeDarwinBulkOpener) {
	t.Helper()
	engine := native.engine
	if len(engine.aborted) != 1 || engine.aborted[0] != 0x81 || engine.closes != 1 || native.closes != 1 {
		t.Fatalf("cleanup = aborted %#v engine %d interface %d", engine.aborted, engine.closes, native.closes)
	}
}

func TestClaimedInterfaceRejectsAlternateSettingWithPendingTransfers(t *testing.T) {
	engine := &fakeDarwinBulkEngine{isPending: true}
	claim := &ClaimedInterface{device: &Device{}, transfers: engine}
	if err := claim.SetAltSetting(1); err == nil {
		t.Fatal("SetAltSetting succeeded")
	}
}

func TestClaimedInterfaceClosesIdleTransferEngineBeforeAlternateSetting(t *testing.T) {
	engine := &fakeDarwinBulkEngine{}
	native := &fakeDarwinBulkOpener{engine: engine}
	device := &Device{iface: native}
	claim := &ClaimedInterface{device: device, transfers: engine, endpoints: map[uint8]Endpoint{0x81: {Address: 0x81}}}
	device.claim = claim
	if err := claim.SetAltSetting(1); err != nil {
		t.Fatal(err)
	}
	if engine.closes != 1 || claim.transfers != nil || claim.endpoints != nil || claim.alternate != 1 {
		t.Fatalf("claim after alternate setting = %#v, engine closes %d", claim, engine.closes)
	}
	if len(native.alternates) != 1 || native.alternates[0] != 1 {
		t.Fatalf("native alternate settings = %#v", native.alternates)
	}
}

func TestDarwinFailedAbortSkipsDrainAndCanBeRetried(t *testing.T) {
	want := errors.New("abort failed")
	drains := 0
	if err := abortAndDrainDarwin(func() error { return want }, func() error { drains++; return nil }); !errors.Is(err, want) {
		t.Fatalf("first abort error = %v, want %v", err, want)
	}
	if drains != 0 {
		t.Fatalf("drains after failed abort = %d, want 0", drains)
	}
	if err := abortAndDrainDarwin(func() error { return nil }, func() error { drains++; return nil }); err != nil {
		t.Fatal(err)
	}
	if drains != 1 {
		t.Fatalf("drains after retried abort = %d, want 1", drains)
	}
}

func TestDarwinDrainDeadlineRetainsPendingTransfer(t *testing.T) {
	polls := 0
	err := waitForDarwinDrain(func() bool { return true }, func() { polls++ }, time.Now().Add(-time.Millisecond))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain error = %v, want %v", err, context.DeadlineExceeded)
	}
	if polls != 0 {
		t.Fatalf("polls after expired deadline = %d, want 0", polls)
	}
}

func TestDarwinFailedDrainCanBeRetried(t *testing.T) {
	want := context.DeadlineExceeded
	drains := 0
	drain := func() error {
		drains++
		if drains == 1 {
			return want
		}
		return nil
	}
	if err := abortAndDrainDarwin(func() error { return nil }, drain); !errors.Is(err, want) {
		t.Fatalf("first drain error = %v, want %v", err, want)
	}
	if err := abortAndDrainDarwin(func() error { return nil }, drain); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinClaimCloseRetriesFailedTransferAbort(t *testing.T) {
	want := errors.New("abort failed")
	engine := &fakeDarwinBulkEngine{closeErrs: []error{want, nil}}
	native := &fakeDarwinBulkOpener{engine: engine}
	device := &Device{iface: native}
	claim := &ClaimedInterface{device: device, transfers: engine}
	device.claim = claim
	if err := claim.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() error = %v, want %v", err, want)
	}
	if device.claim != claim || claim.transfers != engine {
		t.Fatal("failed transfer abort discarded interface ownership")
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
	if device.claim != nil || claim.transfers != nil {
		t.Fatal("retried close retained interface ownership")
	}
}
