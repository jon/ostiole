package ftdi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jon/ostiole/usb"
)

type fakeUSBDevice struct {
	identity        usb.DeviceInfo
	claimed         uint8
	released        uint8
	request         uint8
	value           uint16
	index           uint16
	wroteEP         uint8
	readEP          uint8
	closed          bool
	writeN          []int
	readData        [][]byte
	controls        []controlRecord
	writes          [][]byte
	claimErr        error
	releaseErr      error
	claim           *fakeUSBClaim
	writeErr        int
	writesN         int
	readErr         error
	readWaitGates   []<-chan struct{}
	readsN          int
	releases        int
	submittedReads  int
	maxPendingReads int
	outReadDepth    []int
	aborts          int
	abortErr        error
	abortErrEP      uint8
	abortedEP       []uint8
	pendingOUT      int
	pendingReads    []*fakeUSBTransfer
	mu              sync.Mutex
}

type fakeUSBTransfer struct {
	buffer []byte
	done   chan struct{}
	gate   <-chan struct{}
	once   sync.Once
	count  int
	err    error
}

func (t *fakeUSBTransfer) Wait(ctx context.Context) (int, error) {
	select {
	case <-t.done:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if t.gate != nil {
		select {
		case <-t.gate:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return t.count, t.err
}

func (t *fakeUSBTransfer) complete(count int, err error) {
	t.once.Do(func() {
		t.count, t.err = count, err
		close(t.done)
	})
}

func claimFakeChannel(t *testing.T, channel *Channel) {
	t.Helper()
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.openUSBTransfers(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := channel.releaseUSB(); err != nil {
			t.Errorf("release fake USB channel: %v", err)
		}
	})
}

func TestChannelOpensAContinuousReceiveWindow(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.openUSBTransfers(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw.mu.Lock()
	submitted, pending := raw.submittedReads, len(raw.pendingReads)
	raw.mu.Unlock()
	if submitted != 17 || pending != 17 {
		t.Fatalf("receive ring = submitted %d pending %d", submitted, pending)
	}
	if err := channel.releaseUSB(); err != nil {
		t.Fatal(err)
	}
}

func (d *fakeUSBDevice) Identity() usb.DeviceInfo {
	if d.identity == (usb.DeviceInfo{}) {
		return usb.DeviceInfo{VID: VID, PID: PIDFT232H}
	}
	return d.identity
}

type fakeUSBClaim struct {
	device *fakeUSBDevice
}

func (c *fakeUSBClaim) Endpoint(_ context.Context, address uint8) (usb.Endpoint, error) {
	return usb.Endpoint{Address: address, TransferType: usb.TransferBulk, MaxPacketSize: 512}, nil
}

func (c *fakeUSBClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transfer := &fakeUSBTransfer{buffer: buffer, done: make(chan struct{})}
	if endpoint&0x80 != 0 {
		c.device.mu.Lock()
		if len(c.device.readWaitGates) != 0 {
			transfer.gate = c.device.readWaitGates[0]
			c.device.readWaitGates = c.device.readWaitGates[1:]
		}
		c.device.readEP = endpoint
		c.device.submittedReads++
		c.device.pendingReads = append(c.device.pendingReads, transfer)
		c.device.maxPendingReads = max(c.device.maxPendingReads, len(c.device.pendingReads))
		c.device.mu.Unlock()
		return transfer, nil
	}
	c.device.mu.Lock()
	c.device.outReadDepth = append(c.device.outReadDepth, len(c.device.pendingReads))
	c.device.mu.Unlock()
	count, err := c.device.write(ctx, endpoint, buffer)
	transfer.complete(count, err)
	return transfer, nil
}

func (c *fakeUSBClaim) AbortBulk(endpoint uint8) error {
	c.device.mu.Lock()
	defer c.device.mu.Unlock()
	c.device.aborts++
	c.device.abortedEP = append(c.device.abortedEP, endpoint)
	if c.device.abortErr != nil && (c.device.abortErrEP == 0 || c.device.abortErrEP == endpoint) {
		return c.device.abortErr
	}
	if endpoint&0x80 == 0 {
		c.device.pendingOUT = 0
		return nil
	}
	for _, transfer := range c.device.pendingReads {
		transfer.complete(0, errors.New("injected abort"))
	}
	c.device.pendingReads = nil
	return nil
}

func (c *fakeUSBClaim) Close() error {
	c.device.released = c.device.claimed
	c.device.releases++
	if c.device.releaseErr != nil {
		return c.device.releaseErr
	}
	c.device.claim = nil
	return nil
}

type controlRecord struct {
	request uint8
	value   uint16
	index   uint16
}

func (d *fakeUSBDevice) claimInterface(iface uint8) (usbClaim, error) {
	if d.claimErr != nil {
		return nil, d.claimErr
	}
	d.claimed = iface
	claim := &fakeUSBClaim{device: d}
	d.claim = claim
	return claim, nil
}

func (d *fakeUSBDevice) ControlTransfer(_ context.Context, _ uint8, request uint8, value, index uint16, _ []byte) (int, error) {
	d.request, d.value, d.index = request, value, index
	d.controls = append(d.controls, controlRecord{
		request: request,
		value:   value,
		index:   index,
	})
	return 0, nil
}

func TestChannelOwnsAndClosesMPSSEMode(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	channel.settle = func(context.Context) error { return nil }

	if err := channel.enterMPSSE(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
	want := []controlRecord{
		{request: 0x00, value: 0, index: 1},
		{request: 0x0b, value: 0, index: 1},
		{request: 0x00, value: 1, index: 1},
		{request: 0x00, value: 2, index: 1},
		{request: 0x09, value: 2, index: 1},
		{request: 0x0b, value: 0x0200, index: 1},
		{request: 0x0b, value: 0, index: 1},
		{request: 0x09, value: 16, index: 1},
		{request: 0x00, value: 1, index: 1},
		{request: 0x00, value: 2, index: 1},
	}
	if len(raw.controls) != len(want) {
		t.Fatalf("control requests = %#v", raw.controls)
	}
	for index := range want {
		if raw.controls[index] != want[index] {
			t.Fatalf("control request %d = %#v", index, raw.controls[index])
		}
	}
	if raw.claimed != 0 || raw.released != 0 || !raw.closed {
		t.Fatalf("ownership = %#v", raw)
	}
}

func TestChannelRetainsUSBClaimAfterFailedRelease(t *testing.T) {
	want := errors.New("release failed")
	raw := &fakeUSBDevice{releaseErr: want}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() error = %v, want %v", err, want)
	}
	if raw.closed {
		t.Fatal("failed interface release closed the USB device")
	}
	raw.releaseErr = nil
	if err := channel.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if !raw.closed || raw.releases != 2 {
		t.Fatalf("ownership after retry = %#v", raw)
	}
}

func TestChannelRetriesFailedReceiveAbort(t *testing.T) {
	want := errors.New("abort failed")
	raw := &fakeUSBDevice{abortErr: want}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.openUSBTransfers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := channel.releaseUSB(); !errors.Is(err, want) {
		t.Fatalf("first releaseUSB() error = %v, want %v", err, want)
	}
	if channel.claim == nil || raw.releases != 0 {
		t.Fatalf("failed abort released ownership: %#v", raw)
	}
	raw.abortErr = nil
	if err := channel.releaseUSB(); err != nil {
		t.Fatal(err)
	}
	if channel.claim != nil || raw.releases != 1 {
		t.Fatalf("retried release ownership: %#v", raw)
	}
}

func TestChannelDrainsBulkOUTBeforeRestoringAdapterState(t *testing.T) {
	want := errors.New("OUT abort failed")
	raw := &fakeUSBDevice{abortErr: want, abortErrEP: 0x02, pendingOUT: 1}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.openUSBTransfers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() error = %v, want %v", err, want)
	}
	if len(raw.controls) != 0 || raw.pendingOUT != 1 || raw.releases != 0 || raw.closed {
		t.Fatalf("restoration after failed OUT abort = controls %#v pending %d releases %d closed %t", raw.controls, raw.pendingOUT, raw.releases, raw.closed)
	}
	raw.abortErr = nil
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
	if len(raw.abortedEP) < 3 || raw.abortedEP[0] != 0x02 || raw.abortedEP[1] != 0x02 || raw.abortedEP[2] != 0x81 {
		t.Fatalf("aborted endpoints = %#v", raw.abortedEP)
	}
	if len(raw.controls) != 4 || raw.pendingOUT != 0 || raw.releases != 1 || !raw.closed {
		t.Fatalf("restoration after retry = controls %#v pending %d releases %d closed %t", raw.controls, raw.pendingOUT, raw.releases, raw.closed)
	}
}

func (d *fakeUSBDevice) write(_ context.Context, endpoint uint8, data []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writesN++
	if d.writeErr == d.writesN {
		return 0, errors.New("injected write failure")
	}
	d.wroteEP = endpoint
	d.writes = append(d.writes, append([]byte(nil), data...))
	d.completeReads()
	if len(d.writeN) != 0 {
		count := d.writeN[0]
		d.writeN = d.writeN[1:]
		return count, nil
	}
	return len(data), nil
}

func (d *fakeUSBDevice) completeReads() {
	if len(d.pendingReads) == 0 {
		return
	}
	if d.readErr != nil {
		transfer := d.pendingReads[0]
		d.pendingReads = d.pendingReads[1:]
		d.readsN++
		transfer.complete(0, d.readErr)
		return
	}
	for len(d.pendingReads) != 0 && len(d.readData) != 0 {
		transfer := d.pendingReads[0]
		data := d.readData[0]
		d.pendingReads = d.pendingReads[1:]
		d.readData = d.readData[1:]
		d.readsN++
		copy(transfer.buffer, data)
		transfer.complete(len(data), nil)
	}
}

func (d *fakeUSBDevice) failFirstRead(err error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pendingReads) == 0 {
		return false
	}
	transfer := d.pendingReads[0]
	d.pendingReads = d.pendingReads[1:]
	d.readsN++
	transfer.complete(0, err)
	return true
}

func TestChannelSynchronizesTheMPSSECommandStream(t *testing.T) {
	raw := &fakeUSBDevice{
		readData: [][]byte{{0x01, 0x60, 0xfa, 0xab}},
	}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, channel)

	if err := channel.synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(raw.writes) != 1 ||
		string(raw.writes[0]) != string([]byte{0xab, 0x87}) {
		t.Fatalf("synchronization writes = %x", raw.writes)
	}
}

func TestChannelConfiguresAConservativeMPSSEClock(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, channel)

	if err := channel.configure(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x8a,
		0x97,
		0x8d,
		0x86, 74, 0,
		0x85,
		0x80, 0, 1,
		0x82, 0, 0,
	}
	if len(raw.writes) != 1 ||
		string(raw.writes[0]) != string(want) {
		t.Fatalf("configuration writes = %x, want %x", raw.writes, want)
	}
}

func TestChannelExchangesExactMPSSEPayloads(t *testing.T) {
	raw := &fakeUSBDevice{
		writeN: []int{2, 2},
		readData: [][]byte{
			{0x01, 0x60},
			{0x01, 0x60, 0xaa, 0xbb},
			{0x01, 0x60, 0xcc},
		},
	}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, channel)

	if err := channel.beginResponse(); err != nil {
		t.Fatal(err)
	}
	defer channel.endResponse()
	if err := channel.writeExact(context.Background(), []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	got, err := channel.readPayload(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string([]byte{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("ReadPayload() = %x", got)
	}
}

func TestChannelExchangesThroughExplicitBulkEndpoints(t *testing.T) {
	raw := &fakeUSBDevice{readData: [][]byte{{0x01, 0x60, 0xaa, 0xbb}}}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.openUSBTransfers(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := channel.exchangePayload(context.Background(), []byte{1, 2, 3}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string([]byte{0xaa, 0xbb}) || raw.writesN != 1 || raw.readsN != 1 {
		t.Fatalf("exchange = %x, writes=%d reads=%d", got, raw.writesN, raw.readsN)
	}
	if err := channel.releaseUSB(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelConsumesBulkINTransfersInSubmissionOrder(t *testing.T) {
	firstMayReturn := make(chan struct{})
	raw := &fakeUSBDevice{
		readData:      [][]byte{{0x01, 0x60, 0xaa}, {0x01, 0x60, 0xbb}},
		readWaitGates: []<-chan struct{}{firstMayReturn},
	}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, channel)
	if err := channel.beginResponse(); err != nil {
		t.Fatal(err)
	}
	defer channel.endResponse()
	if err := channel.writeExact(context.Background(), []byte{1}); err != nil {
		t.Fatal(err)
	}
	var premature []byte
	select {
	case payload := <-channel.receive:
		premature = payload
	case <-time.After(20 * time.Millisecond):
	}
	close(firstMayReturn)
	if premature != nil {
		t.Fatalf("payload completed out of submission order: %x", premature)
	}
	select {
	case payload := <-channel.receive:
		if string(payload) != "\xaa" {
			t.Fatalf("first payload = %x, want aa", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first payload")
	}
	select {
	case payload := <-channel.receive:
		if string(payload) != "\xbb" {
			t.Fatalf("second payload = %x, want bb", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second payload")
	}
}

func TestChannelRearmsTheReceiveRingBeforeDeliveringPayload(t *testing.T) {
	raw := &fakeUSBDevice{readData: [][]byte{{0x01, 0x60, 0xaa}}}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, channel)
	first, err := channel.exchangePayload(context.Background(), []byte{1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw.mu.Lock()
	raw.readData = append(raw.readData, []byte{0x01, 0x60, 0xbb})
	raw.mu.Unlock()
	second, err := channel.exchangePayload(context.Background(), []byte{2}, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw.mu.Lock()
	depths := append([]int(nil), raw.outReadDepth...)
	raw.mu.Unlock()
	if string(first) != "\xaa" || string(second) != "\xbb" || len(depths) != 2 || depths[0] != 17 || depths[1] != 17 {
		t.Fatalf("exchanges = %x then %x, pending depths %#v", first, second, depths)
	}
}

func (d *fakeUSBDevice) Close() error {
	if d.claim != nil {
		if err := d.claim.Close(); err != nil {
			return err
		}
	}
	d.closed = true
	return nil
}

func TestChannelBindsOneExplicitMPSSEPort(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.openUSBTransfers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.control(context.Background(), 0x0b, 0x0200); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.bulkWrite(context.Background(), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := channel.releaseUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
	if raw.claimed != 0 || raw.released != 0 ||
		raw.request != 0x0b || raw.value != 0x0200 || raw.index != 1 ||
		raw.wroteEP != 0x02 || raw.readEP != 0x81 || !raw.closed {
		t.Fatalf("forwarded operations = %#v", raw)
	}
}

func TestChannelRejectsUnsupportedSelections(t *testing.T) {
	for _, port := range []Port{PortUnspecified, Port(3)} {
		if _, err := newChannel(&fakeUSBDevice{}, Config{Port: port, MaxClockHz: 400_000}); err == nil {
			t.Fatalf("newChannel(Port(%d)) succeeded", port)
		}
	}
}

func TestChannelDerivesProductFromUSBIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity usb.DeviceInfo
		port     Port
	}{
		{name: "another vendor", identity: usb.DeviceInfo{VID: 0x1234, PID: PIDFT232H}, port: PortA},
		{name: "unsupported product", identity: usb.DeviceInfo{VID: VID, PID: 0xffff}, port: PortA},
		{name: "FT232H port B", identity: usb.DeviceInfo{VID: VID, PID: PIDFT232H}, port: PortB},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newChannel(&fakeUSBDevice{identity: test.identity}, Config{Port: test.port, MaxClockHz: 400_000}); err == nil {
				t.Fatal("newChannel() succeeded")
			}
		})
	}
}
