//go:build linux

package usb

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type linuxTransferIOCTL struct {
	mu                        sync.Mutex
	poller                    *fakeLinuxBulkPoller
	submitted                 []uint8
	pending                   map[*usbURB]bool
	completed                 []*usbURB
	discarded                 int
	discardErr                error
	discardErrs               int
	nonblockingReapErr        error
	nonblockingReapErrs       int
	nonblockingReaps          int
	withholdDiscardCompletion bool
}

func (f *linuxTransferIOCTL) run(_ uintptr, request uintptr, argument any) (uintptr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch request {
	case usbfsSubmitURB:
		urb := argument.(*usbURB)
		f.submitted = append(f.submitted, urb.Endpoint)
		f.pending[urb] = true
		return 0, nil
	case usbfsReapURBNoDelay:
		f.nonblockingReaps++
		if f.nonblockingReapErrs != 0 {
			f.nonblockingReapErrs--
			return 0, f.nonblockingReapErr
		}
		return f.reap(argument, true)
	case usbfsDiscardURB:
		urb := argument.(*usbURB)
		if f.discardErrs != 0 {
			f.discardErrs--
			return 0, f.discardErr
		}
		if !f.pending[urb] {
			return 0, syscall.EINVAL
		}
		f.discarded++
		if f.withholdDiscardCompletion {
			return 0, nil
		}
		urb.Status = -int32(syscall.ENOENT)
		f.completed = append(f.completed, urb)
		f.poller.signal(nil)
		return 0, nil
	case usbfsReleaseInterface:
		return 0, nil
	default:
		return 0, syscall.EINVAL
	}
}

func (f *linuxTransferIOCTL) reap(argument any, nonblocking bool) (uintptr, error) {
	if len(f.completed) == 0 {
		if nonblocking {
			return 0, syscall.EAGAIN
		}
		return 0, syscall.EIO
	}
	urb := f.completed[0]
	f.completed = f.completed[1:]
	delete(f.pending, urb)
	*argument.(**usbURB) = urb
	return 0, nil
}

func (f *linuxTransferIOCTL) complete(endpoint uint8, data []byte) bool {
	return f.completeAndNotify(endpoint, data, true)
}

func (f *linuxTransferIOCTL) completeAndNotify(endpoint uint8, data []byte, notify bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for urb := range f.pending {
		if urb.Endpoint != endpoint {
			continue
		}
		copy(unsafe.Slice((*byte)(urb.Buffer), int(urb.BufferLength)), data)
		urb.ActualLength = int32(len(data))
		f.completed = append(f.completed, urb)
		if notify {
			f.poller.signal(nil)
		}
		return true
	}
	return false
}

func (f *linuxTransferIOCTL) completeTransfer(transfer *BulkTransfer, data []byte, notify bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	backend := transfer.backend.(*linuxBulkTransfer)
	urb := &backend.urb
	if !f.pending[urb] {
		return false
	}
	copy(unsafe.Slice((*byte)(urb.Buffer), int(urb.BufferLength)), data)
	urb.ActualLength = int32(len(data))
	f.completed = append(f.completed, urb)
	if notify {
		f.poller.signal(nil)
	}
	return true
}

func (f *linuxTransferIOCTL) completeCanceled(endpoint uint8) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for urb := range f.pending {
		if urb.Endpoint != endpoint {
			continue
		}
		urb.Status = -int32(syscall.ENOENT)
		f.completed = append(f.completed, urb)
		f.poller.signal(nil)
		return true
	}
	return false
}

type fakeLinuxBulkPoller struct {
	events chan error
	stop   chan struct{}
	once   sync.Once
}

func newFakeLinuxBulkPoller() *fakeLinuxBulkPoller {
	return &fakeLinuxBulkPoller{events: make(chan error, 1), stop: make(chan struct{})}
}

func (p *fakeLinuxBulkPoller) wait() error {
	select {
	case err := <-p.events:
		return err
	case <-p.stop:
		return errLinuxBulkPollerStopped
	}
}

func (p *fakeLinuxBulkPoller) stopWaiting() error {
	p.once.Do(func() { close(p.stop) })
	return nil
}

func (p *fakeLinuxBulkPoller) close() error { return nil }

func (p *fakeLinuxBulkPoller) signal(err error) {
	select {
	case p.events <- err:
	default:
	}
}

func TestLinuxBulkTransfersRemainExplicitAndIndependent(t *testing.T) {
	_, claim, fake := linuxTransferTest(t)
	reads := make([]*BulkTransfer, 2)
	for index := range reads {
		var err error
		reads[index], err = claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := claim.SubmitBulk(context.Background(), 0x02, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	submitted := append([]uint8(nil), fake.submitted...)
	fake.mu.Unlock()
	if len(submitted) != 3 || submitted[0] != 0x81 || submitted[1] != 0x81 || submitted[2] != 0x02 {
		t.Fatalf("submissions = %#v", submitted)
	}
	if !fake.complete(0x02, nil) {
		t.Fatal("no OUT transfer to complete")
	}
	if count, err := out.Wait(context.Background()); err != nil || count != 0 {
		t.Fatalf("OUT Wait() = %d, %v", count, err)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxBulkTransferWaitLeavesRequestPending(t *testing.T) {
	_, claim, fake := linuxTransferTest(t)
	buffer := make([]byte, 8)
	transfer, err := claim.SubmitBulk(context.Background(), 0x81, buffer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transfer.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Wait() error = %v", err)
	}
	fake.mu.Lock()
	pending := len(fake.pending)
	fake.mu.Unlock()
	if pending != 1 {
		t.Fatalf("pending transfers = %d", pending)
	}
	if !fake.complete(0x81, []byte{4, 5}) {
		t.Fatal("no IN transfer to complete")
	}
	if count, err := transfer.Wait(context.Background()); err != nil || count != 2 || string(buffer[:count]) != "\x04\x05" {
		t.Fatalf("second Wait() = %d, %x, %v", count, buffer, err)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxAbortBulkCancelsEveryTransferOnEndpoint(t *testing.T) {
	_, claim, fake := linuxTransferTest(t)
	reads := make([]*BulkTransfer, 3)
	for index := range reads {
		var err error
		reads[index], err = claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := claim.AbortBulk(0x81); err != nil {
		t.Fatal(err)
	}
	for _, transfer := range reads {
		if count, err := transfer.Wait(context.Background()); count != 0 || !errors.Is(err, syscall.ENOENT) {
			t.Fatalf("aborted Wait() = %d, %v", count, err)
		}
	}
	fake.mu.Lock()
	discarded, pending := fake.discarded, len(fake.pending)
	fake.mu.Unlock()
	if discarded != 3 || pending != 0 {
		t.Fatalf("cleanup = discarded %d pending %d", discarded, pending)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxClaimCloseTimesOutAndRetriesMissingTransferCompletion(t *testing.T) {
	device, claim, fake := linuxTransferTest(t)
	fake.withholdDiscardCompletion = true
	transfer, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
	if err != nil {
		t.Fatal(err)
	}
	claim.transfers.(*linuxBulkEngine).cleanupTimeout = 10 * time.Millisecond
	started := time.Now()
	if err := claim.Close(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Close() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("timed-out Close() took %v", elapsed)
	}
	if device.claim != claim {
		t.Fatal("failed drain released the interface")
	}
	select {
	case <-transfer.Done():
		t.Fatal("timed-out drain released the transfer buffer")
	default:
	}
	fake.withholdDiscardCompletion = false
	if !fake.completeCanceled(0x81) {
		t.Fatal("no canceled transfer to complete")
	}
	if _, err := transfer.Wait(context.Background()); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("transfer completion error = %v, want %v", err, syscall.ENOENT)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
	if device.claim != nil {
		t.Fatal("retried close retained the interface")
	}
}

func TestLinuxClaimCloseRetriesFailedTransferAbortWithoutDraining(t *testing.T) {
	want := errors.New("discard failed")
	device, claim, fake := linuxTransferTest(t)
	fake.discardErr, fake.discardErrs = want, 1
	transfer, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() error = %v, want %v", err, want)
	}
	fake.mu.Lock()
	reaps := fake.nonblockingReaps
	fake.mu.Unlock()
	if reaps != 0 {
		t.Fatalf("reaps after failed abort = %d, want 0", reaps)
	}
	if device.claim != claim {
		t.Fatal("failed abort released the interface")
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.Wait(context.Background()); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("transfer completion error = %v, want %v", err, syscall.ENOENT)
	}
}

func TestLinuxReapFailurePoisonsEngineUntilTransfersDrain(t *testing.T) {
	want := errors.New("reap failed")
	_, claim, fake := linuxTransferTest(t)
	transfer, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
	if err != nil {
		t.Fatal(err)
	}
	fake.nonblockingReapErr, fake.nonblockingReapErrs = want, 1
	fake.poller.signal(nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := transfer.Wait(ctx); !errors.Is(err, want) {
		t.Fatalf("Wait() error = %v, want %v", err, want)
	}
	select {
	case <-transfer.Done():
		t.Fatal("reap failure released the transfer buffer")
	default:
	}
	if _, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512)); !errors.Is(err, want) {
		t.Fatalf("SubmitBulk() error = %v, want %v", err, want)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.Wait(context.Background()); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("completed Wait() error = %v, want %v", err, syscall.ENOENT)
	}
}

func TestLinuxTerminalPollDrainsCompletionsAndPoisonsEngine(t *testing.T) {
	want := errLinuxBulkPollerTerminal
	_, claim, fake := linuxTransferTest(t)
	completed, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
	if err != nil {
		t.Fatal(err)
	}
	if !fake.completeTransfer(completed, []byte{1, 2}, false) {
		t.Fatal("no IN transfer to complete")
	}
	fake.poller.signal(want)
	if count, err := completed.Wait(context.Background()); err != nil || count != 2 {
		t.Fatalf("completed Wait() = %d, %v", count, err)
	}
	if _, err := pending.Wait(context.Background()); !errors.Is(err, want) {
		t.Fatalf("pending Wait() error = %v, want %v", err, want)
	}
	select {
	case <-pending.Done():
		t.Fatal("terminal poll released the pending transfer buffer")
	default:
	}
	if _, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512)); !errors.Is(err, want) {
		t.Fatalf("SubmitBulk() error = %v, want %v", err, want)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := pending.Wait(context.Background()); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("completed Wait() error = %v, want %v", err, syscall.ENOENT)
	}
}

func TestLinuxIdleEngineWaitsForCompletionNotification(t *testing.T) {
	_, claim, fake := linuxTransferTest(t)
	transfer, err := claim.SubmitBulk(context.Background(), 0x81, make([]byte, 512))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	fake.mu.Lock()
	reaps := fake.nonblockingReaps
	fake.mu.Unlock()
	if reaps != 0 {
		t.Fatalf("reaps without a completion notification = %d, want 0", reaps)
	}
	if !fake.complete(0x81, []byte{1}) {
		t.Fatal("no IN transfer to complete")
	}
	if _, err := transfer.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := claim.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxUSBURBMatchesNativeLayout(t *testing.T) {
	pointerSize := unsafe.Sizeof(uintptr(0))
	wantSize, wantBuffer, wantContext := uintptr(44), uintptr(12), uintptr(40)
	if pointerSize == 8 {
		wantSize, wantBuffer, wantContext = 56, 16, 48
	}
	if unsafe.Sizeof(usbURB{}) != wantSize || unsafe.Offsetof(usbURB{}.Buffer) != wantBuffer || unsafe.Offsetof(usbURB{}.UserContext) != wantContext {
		t.Fatalf("usbURB layout = size %d buffer %d context %d", unsafe.Sizeof(usbURB{}), unsafe.Offsetof(usbURB{}.Buffer), unsafe.Offsetof(usbURB{}.UserContext))
	}
}

func linuxTransferTest(t *testing.T) (*Device, *ClaimedInterface, *linuxTransferIOCTL) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "usb-device")
	if err != nil {
		t.Fatal(err)
	}
	poller := newFakeLinuxBulkPoller()
	fake := &linuxTransferIOCTL{poller: poller, pending: make(map[*usbURB]bool)}
	device := &Device{file: file, ioctl: fake.run, newBulkPoller: func(uintptr) (linuxBulkPoller, error) {
		return poller, nil
	}}
	claim := linuxTransferTestClaim(device)
	device.claim = claim
	return device, claim, fake
}

func linuxTransferTestClaim(device *Device) *ClaimedInterface {
	return &ClaimedInterface{device: device, number: 0, endpoints: map[uint8]Endpoint{
		0x02: {Address: 0x02, TransferType: TransferBulk, MaxPacketSize: 512},
		0x81: {Address: 0x81, TransferType: TransferBulk, MaxPacketSize: 512},
	}}
}
