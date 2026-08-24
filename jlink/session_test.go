package jlink

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/jon/ostiole/usb"
)

type peerOperation struct {
	request  []byte
	response [][]byte
}

type peerUSBDevice struct {
	t                 *testing.T
	identity          usb.DeviceInfo
	configuration     usb.Configuration
	configurationErrs []error
	operations        []peerOperation
	endpoints         map[uint8]usb.Endpoint
	writeBuffer       []byte
	responses         [][]byte
	submissions       []uint8
	endpointCalls     []uint8
	writeLimit        int
	writes, reads     int
	aborts            int
	configurationN    int
	claimed, selected uint8
	releases, closes  int
	claimErr          error
	selectErr         error
	releaseErr        error
	writeErr          error
	readErr           error
	readCount         int
	closeErr          error
	afterWrite        func()
	afterReadWait     func()
}

type peerUSBClaim struct{ device *peerUSBDevice }

type peerUSBTransfer struct {
	done      chan struct{}
	count     int
	err       error
	afterWait func()
}

type cancelUSBClaim struct {
	transfer *cancelUSBTransfer
	abortErr error
	aborts   int
}

type cancelUSBTransfer struct {
	waiting  chan struct{}
	done     chan struct{}
	firstErr error
	err      error
	cancel   func()
	waits    int
}

func (d *peerUSBDevice) Identity() usb.DeviceInfo { return d.identity }

func (d *peerUSBDevice) ActiveConfiguration(context.Context) (usb.Configuration, error) {
	d.configurationN++
	if len(d.configurationErrs) != 0 {
		err := d.configurationErrs[0]
		d.configurationErrs = d.configurationErrs[1:]
		if err != nil {
			return usb.Configuration{}, err
		}
	}
	return d.configuration, nil
}

func (d *peerUSBDevice) claimInterface(number uint8) (usbClaim, error) {
	if d.claimErr != nil {
		return nil, d.claimErr
	}
	d.claimed = number
	return &peerUSBClaim{device: d}, nil
}

func (c *peerUSBClaim) SetAltSetting(alternate uint8) error {
	if c.device.selectErr != nil {
		return c.device.selectErr
	}
	c.device.selected = alternate
	return nil
}

func (c *peerUSBClaim) Endpoint(ctx context.Context, address uint8) (usb.Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return usb.Endpoint{}, err
	}
	c.device.endpointCalls = append(c.device.endpointCalls, address)
	endpoint, ok := c.device.endpoints[address]
	if !ok {
		return usb.Endpoint{}, fmt.Errorf("missing endpoint %#02x", address)
	}
	return endpoint, nil
}

func (c *peerUSBClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.device.submissions = append(c.device.submissions, endpoint)
	transfer := &peerUSBTransfer{done: make(chan struct{})}
	if endpoint&0x80 == 0 {
		transfer.count, transfer.err = c.device.write(buffer)
	} else {
		transfer.count, transfer.err = c.device.read(buffer)
		if c.device.readCount != 0 {
			transfer.count = c.device.readCount
		}
		transfer.afterWait = c.device.afterReadWait
	}
	close(transfer.done)
	return transfer, nil
}

func TestSessionSubmitsInputOnlyAfterOutputCompletes(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{request: []byte{1}, response: [][]byte{{2}}}})
	device.afterWrite = func() {
		if !reflect.DeepEqual(device.submissions, []uint8{0x03}) {
			t.Fatalf("submissions at OUT completion = %#v", device.submissions)
		}
	}
	session := &Session{device: device, claim: &peerUSBClaim{device: device}, application: applicationInterface{
		bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 512}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	response, err := session.exchange(context.Background(), []byte{1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response, []byte{2}) || !reflect.DeepEqual(device.submissions, []uint8{0x03, 0x84}) {
		t.Fatalf("exchange = response %x submissions %#v", response, device.submissions)
	}
}

func TestSessionPoisonsCancellationAfterCommandWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	device := metadataPeer(t, []peerOperation{{request: []byte{1}, response: [][]byte{{2}}}})
	device.afterWrite = cancel
	session := &Session{device: device, claim: &peerUSBClaim{device: device}, application: applicationInterface{
		bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 512}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	if _, err := session.exchange(ctx, []byte{1}, 1); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("exchange() error = %v", err)
	}
	if !reflect.DeepEqual(device.submissions, []uint8{0x03}) {
		t.Fatalf("submissions = %#v, want command OUT only", device.submissions)
	}
	if _, err := session.exchange(context.Background(), []byte{1}, 1); !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("exchange() after cancellation error = %v", err)
	}
	if !reflect.DeepEqual(device.submissions, []uint8{0x03}) {
		t.Fatalf("poisoned session submissions = %#v", device.submissions)
	}
}

func TestSessionPoisonsCancellationBetweenFirmwareLengthAndRecord(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{request: []byte{commandVersion}, response: [][]byte{{2, 0}, {'v', 0}}}})
	session := &Session{device: device, claim: &peerUSBClaim{device: device}, application: applicationInterface{
		bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 512}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	device.afterReadWait = cancel
	if err := session.readVersion(ctx); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("firmware record error = %v", err)
	}
	wantSubmissions := []uint8{0x03, 0x84}
	if !reflect.DeepEqual(device.submissions, wantSubmissions) {
		t.Fatalf("submissions = %#v, want %#v", device.submissions, wantSubmissions)
	}
	if _, err := session.exchange(context.Background(), []byte{1}, 1); !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("exchange() after cancellation error = %v", err)
	}
	if !reflect.DeepEqual(device.submissions, wantSubmissions) {
		t.Fatalf("poisoned session submissions = %#v", device.submissions)
	}
}

func TestSessionAcceptsZeroLengthCompletionsSeparatedByProgress(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{request: []byte{1}, response: [][]byte{nil, {2}, nil, {3}}}})
	session := &Session{device: device, claim: &peerUSBClaim{device: device}, application: applicationInterface{
		bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 512}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	response, err := session.exchange(context.Background(), []byte{1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response, []byte{2, 3}) || session.poisoned || device.reads != 4 {
		t.Fatalf("response = %x, poisoned %t, reads %d", response, session.poisoned, device.reads)
	}
}

func TestSessionPoisonsInvalidBulkReadCounts(t *testing.T) {
	for name, count := range map[string]int{"negative": -1, "too large": 513} {
		t.Run(name, func(t *testing.T) {
			device := metadataPeer(t, []peerOperation{{request: []byte{1}, response: [][]byte{{2}}}})
			device.readCount = count
			session := &Session{device: device, claim: &peerUSBClaim{device: device}, application: applicationInterface{
				bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 512}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
			}}
			if _, err := session.exchange(context.Background(), []byte{1}, 1); !errors.Is(err, ErrSessionPoisoned) || !strings.Contains(err.Error(), "invalid bulk-read count") {
				t.Fatalf("exchange() error = %v", err)
			}
		})
	}
}

func TestSessionPoisonsConsecutiveZeroLengthCompletions(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{request: []byte{1}, response: [][]byte{nil, nil}}})
	session := &Session{device: device, claim: &peerUSBClaim{device: device}, application: applicationInterface{
		bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 512}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	if _, err := session.exchange(context.Background(), []byte{1}, 1); !errors.Is(err, io.ErrNoProgress) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("exchange() error = %v", err)
	}
	if device.reads != 2 {
		t.Fatalf("bulk reads = %d, want 2", device.reads)
	}
}

func (c *peerUSBClaim) AbortBulk(uint8) error {
	c.device.aborts++
	return nil
}

func (c *peerUSBClaim) Close() error {
	c.device.releases++
	if c.device.releaseErr != nil {
		return c.device.releaseErr
	}
	return nil
}

func (d *peerUSBDevice) write(data []byte) (int, error) {
	d.writes++
	if d.writeErr != nil {
		return 0, d.writeErr
	}
	if len(d.operations) == 0 {
		d.t.Fatalf("unexpected bulk write %x", data)
	}
	count := len(data)
	if d.writeLimit > 0 && count > d.writeLimit {
		count = d.writeLimit
	}
	d.writeBuffer = append(d.writeBuffer, data[:count]...)
	want := d.operations[0].request
	if len(d.writeBuffer) > len(want) || !reflect.DeepEqual(d.writeBuffer, want[:len(d.writeBuffer)]) {
		d.t.Fatalf("request prefix = %x, want %x", d.writeBuffer, want)
	}
	if len(d.writeBuffer) == len(want) {
		for _, response := range d.operations[0].response {
			d.responses = append(d.responses, append([]byte(nil), response...))
		}
		d.operations = d.operations[1:]
		d.writeBuffer = nil
		if d.afterWrite != nil {
			d.afterWrite()
		}
	}
	return count, nil
}

func (d *peerUSBDevice) read(buffer []byte) (int, error) {
	d.reads++
	if d.readErr != nil {
		return 0, d.readErr
	}
	if len(d.responses) == 0 {
		d.t.Fatal("bulk read without a queued response")
	}
	response := d.responses[0]
	d.responses = d.responses[1:]
	count := copy(buffer, response)
	if count != len(response) {
		d.t.Fatalf("%d-byte IN transfer cannot hold %d-byte response", len(buffer), len(response))
	}
	return count, nil
}

func (t *peerUSBTransfer) Wait(ctx context.Context) (int, error) {
	select {
	case <-t.done:
		return t.completion()
	default:
	}
	select {
	case <-t.done:
		return t.completion()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (t *peerUSBTransfer) completion() (int, error) {
	if t.afterWait != nil {
		t.afterWait()
		t.afterWait = nil
	}
	return t.count, t.err
}

func (c *cancelUSBClaim) SetAltSetting(uint8) error { return nil }

func (c *cancelUSBClaim) Endpoint(context.Context, uint8) (usb.Endpoint, error) {
	return usb.Endpoint{}, errors.New("unexpected endpoint lookup")
}

func (c *cancelUSBClaim) SubmitBulk(context.Context, uint8, []byte) (usbBulkTransfer, error) {
	return c.transfer, nil
}

func (c *cancelUSBClaim) AbortBulk(uint8) error {
	c.aborts++
	if c.abortErr != nil {
		return c.abortErr
	}
	close(c.transfer.done)
	return nil
}

func (c *cancelUSBClaim) Close() error { return nil }

func (t *cancelUSBTransfer) Wait(ctx context.Context) (int, error) {
	t.waits++
	if t.waits == 1 {
		if t.waiting != nil {
			close(t.waiting)
		}
		if t.firstErr != nil {
			if t.cancel != nil {
				t.cancel()
			}
			return 0, t.firstErr
		}
	}
	select {
	case <-t.done:
		return 0, t.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (d *peerUSBDevice) Close() error {
	d.closes++
	return d.closeErr
}

func TestInspectApplicationRetriesUnconfiguredState(t *testing.T) {
	device := metadataPeer(t, nil)
	device.configurationErrs = []error{usb.ErrNotConfigured, fmt.Errorf("transition: %w", usb.ErrNotConfigured), nil}
	waits := 0
	application, err := inspectApplicationWithWait(context.Background(), device, func(context.Context) error {
		waits++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if application.number != 3 || application.alternate != 2 || device.configurationN != 3 || waits != 2 {
		t.Fatalf("application = %#v, inspections %d waits %d", application, device.configurationN, waits)
	}
}

func TestInspectApplicationBoundsUnconfiguredRetries(t *testing.T) {
	device := metadataPeer(t, nil)
	device.configurationErrs = make([]error, configurationInspectionAttempts)
	for i := range device.configurationErrs {
		device.configurationErrs[i] = usb.ErrNotConfigured
	}
	waits := 0
	_, err := inspectApplicationWithWait(context.Background(), device, func(context.Context) error {
		waits++
		return nil
	})
	if !errors.Is(err, usb.ErrNotConfigured) {
		t.Fatalf("inspectApplicationWithWait error = %v, want ErrNotConfigured", err)
	}
	if device.configurationN != configurationInspectionAttempts || waits != configurationInspectionAttempts-1 {
		t.Fatalf("inspections = %d waits %d", device.configurationN, waits)
	}
}

func TestInspectApplicationPreservesUnconfiguredStateWhenRetryEnds(t *testing.T) {
	t.Run("wait", func(t *testing.T) {
		device := metadataPeer(t, nil)
		device.configurationErrs = []error{usb.ErrNotConfigured}
		want := context.Canceled
		_, err := inspectApplicationWithWait(context.Background(), device, func(context.Context) error { return want })
		if !errors.Is(err, usb.ErrNotConfigured) || !errors.Is(err, want) {
			t.Fatalf("inspectApplicationWithWait error = %v, want both errors", err)
		}
	})
	t.Run("inspection", func(t *testing.T) {
		device := metadataPeer(t, nil)
		device.configurationErrs = []error{usb.ErrNotConfigured, context.DeadlineExceeded}
		_, err := inspectApplicationWithWait(context.Background(), device, func(context.Context) error { return nil })
		if !errors.Is(err, usb.ErrNotConfigured) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("inspectApplicationWithWait error = %v, want both errors", err)
		}
	})
}

func TestOpenReadsMetadataWithoutSelectingATargetInterface(t *testing.T) {
	record := []byte{'J', '-', 'L', 'i', 'n', 'k', ' ', 'F', 'W', 0, 'x', 0}
	short := []byte{0x02, 0x08, 0x02, 0x80}
	long := make([]byte, 32)
	copy(long, short)
	long[7] = 0x40
	hardware := make([]byte, 4)
	binary.LittleEndian.PutUint32(hardware, 3_120_405)
	workspace := make([]byte, 4)
	binary.LittleEndian.PutUint32(workspace, 11_608)
	version := append([]byte{byte(len(record)), 0}, record...)
	device := metadataPeer(t, []peerOperation{
		{request: []byte{0x01}, response: [][]byte{version}},
		{request: []byte{0xe8}, response: [][]byte{short}},
		{request: []byte{0xed}, response: [][]byte{long[:15], long[15:]}},
		{request: []byte{0xf0}, response: [][]byte{hardware}},
		{request: []byte{0xd4}, response: [][]byte{workspace}},
		{request: []byte{0xc7, 0xff}, response: [][]byte{{0x83, 0, 0, 0}}},
		{request: []byte{0xc7, 0xfe}, response: [][]byte{{0, 0, 0, 0}}},
	})
	device.writeLimit = 1

	session, err := openSession(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info()
	assertMetadata(t, info, device.identity, record)
	assertDetachedInfo(t, session, info)
	if device.claimed != 3 || device.selected != 2 || len(device.operations) != 0 {
		t.Fatalf("USB selection = interface %d alternate %d operations left %d", device.claimed, device.selected, len(device.operations))
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if device.releases != 1 || device.closes != 1 {
		t.Fatalf("cleanup = releases %d closes %d", device.releases, device.closes)
	}
}

func TestOpenUsesSelectedActiveEndpointProperties(t *testing.T) {
	device := metadataPeer(t, []peerOperation{
		{request: []byte{commandVersion}, response: [][]byte{{0, 0}}},
		{request: []byte{commandCapabilitiesShort}, response: [][]byte{{0, 0, 0, 0}}},
	})
	device.endpoints[0x84] = usb.Endpoint{Address: 0x84, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	device.endpoints[0x03] = usb.Endpoint{Address: 0x03, TransferType: usb.TransferBulk, MaxPacketSize: 64}

	session, err := openSession(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	}()
	wantCalls := []uint8{0x84, 0x03}
	if !reflect.DeepEqual(device.endpointCalls, wantCalls) {
		t.Fatalf("endpoint calls = %#v, want %#v", device.endpointCalls, wantCalls)
	}
	if session.application.bulkIn.MaxPacketSize != 64 || session.application.bulkOut.MaxPacketSize != 64 {
		t.Fatalf("active endpoints = IN %#v OUT %#v", session.application.bulkIn, session.application.bulkOut)
	}
}

func TestOpenRejectsInvalidSelectedActiveEndpointsBeforeTraffic(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*peerUSBDevice)
		want      string
	}{
		{
			name: "missing IN",
			configure: func(device *peerUSBDevice) {
				delete(device.endpoints, 0x84)
			},
			want: "resolve active bulk IN endpoint 0x84",
		},
		{
			name: "invalid OUT",
			configure: func(device *peerUSBDevice) {
				device.endpoints[0x03] = usb.Endpoint{Address: 0x03, TransferType: usb.TransferInterrupt, MaxPacketSize: 64}
			},
			want: "invalid active bulk OUT endpoint 0x03",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			device := metadataPeer(t, nil)
			test.configure(device)
			session, err := openSession(context.Background(), device)
			if session != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("openSession() = (%T, %v), want nil and %q", session, err, test.want)
			}
			if device.writes != 0 || device.releases != 1 || device.closes != 1 {
				t.Fatalf("traffic and cleanup = writes %d releases %d closes %d", device.writes, device.releases, device.closes)
			}
		})
	}
}

func assertMetadata(t *testing.T, info Info, identity usb.DeviceInfo, record []byte) {
	t.Helper()
	if info.USB != identity || info.Firmware != "J-Link FW" || !reflect.DeepEqual(info.FirmwareRecord, record) {
		t.Fatalf("version info = %#v", info)
	}
	if !info.HardwareKnown || info.Hardware != (HardwareVersion{Raw: 3_120_405, Type: 3, Major: 12, Minor: 4, Revision: 5}) {
		t.Fatalf("hardware = %#v, known %t", info.Hardware, info.HardwareKnown)
	}
	if !info.Capabilities.Has(31) || !info.Capabilities.Has(62) || info.Capabilities.BitLen() != 256 {
		t.Fatalf("capabilities = %x", info.Capabilities.Bytes())
	}
	if !info.WorkspaceKnown || info.Workspace != 11_608 || info.AvailableInterfaces != 0x83 || !info.SelectedInterfaceKnown || info.SelectedInterface != 0 {
		t.Fatalf("session metadata = %#v", info)
	}
}

func assertDetachedInfo(t *testing.T, session *Session, info Info) {
	t.Helper()
	info.FirmwareRecord[0] = 0
	capabilities := info.Capabilities.Bytes()
	capabilities[0] = 0
	if session.Info().FirmwareRecord[0] != 'J' || !session.Info().Capabilities.Has(1) {
		t.Fatal("Info returned mutable session storage")
	}
}

func TestOpenSkipsCapabilityGatedQueries(t *testing.T) {
	device := metadataPeer(t, []peerOperation{
		{request: []byte{0x01}, response: [][]byte{{1, 0}, {'v'}}},
		{request: []byte{0xe8}, response: [][]byte{{1, 0, 0, 0}}},
	})
	session, err := openSession(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	}()
	info := session.Info()
	if info.HardwareKnown || info.WorkspaceKnown || info.SelectedInterfaceKnown || info.Capabilities.BitLen() != 32 {
		t.Fatalf("Info() = %#v", info)
	}
}

func TestOpenRejectsInconsistentExtendedCapabilitiesAndCleansUp(t *testing.T) {
	short := []byte{0, 0, 0, 0x80}
	long := make([]byte, 32)
	device := metadataPeer(t, []peerOperation{
		{request: []byte{0x01}, response: [][]byte{{0, 0}}},
		{request: []byte{0xe8}, response: [][]byte{short}},
		{request: []byte{0xed}, response: [][]byte{long}},
	})
	if session, err := openSession(context.Background(), device); session != nil || err == nil {
		t.Fatalf("openSession() = %T, %v", session, err)
	}
	if device.releases != 1 || device.closes != 1 {
		t.Fatalf("cleanup = releases %d closes %d", device.releases, device.closes)
	}
}

func TestOpenRejectsOutOfDomainSelectedInterface(t *testing.T) {
	device := metadataPeer(t, []peerOperation{
		{request: []byte{commandVersion}, response: [][]byte{{0, 0}}},
		{request: []byte{commandCapabilitiesShort}, response: [][]byte{{0, 0, 2, 0}}},
		{request: []byte{commandInterface, 0xff}, response: [][]byte{{0xff, 0xff, 0xff, 0xff}}},
		{request: []byte{commandInterface, 0xfe}, response: [][]byte{{32, 0, 0, 0}}},
	})
	if session, err := openSession(context.Background(), device); session != nil || err == nil || !strings.Contains(err.Error(), "invalid selected target interface 32") {
		t.Fatalf("openSession() = %T, %v", session, err)
	}
	if len(device.operations) != 0 || device.releases != 1 || device.closes != 1 {
		t.Fatalf("cleanup = operations %d releases %d closes %d", len(device.operations), device.releases, device.closes)
	}
}

func TestOpenRejectsIdentityAndDescriptorBeforeClaim(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		device := metadataPeer(t, nil)
		device.identity.PID = 0x1008
		if _, err := openSession(context.Background(), device); err == nil {
			t.Fatal("openSession() succeeded")
		}
		if device.configurationN != 0 || device.claimed != 0 || device.closes != 1 {
			t.Fatalf("device activity = %#v", device)
		}
	})
	t.Run("descriptor", func(t *testing.T) {
		device := metadataPeer(t, nil)
		device.configuration.Interfaces = nil
		if _, err := openSession(context.Background(), device); err == nil {
			t.Fatal("openSession() succeeded")
		}
		if device.claimed != 0 || device.writes != 0 || device.closes != 1 {
			t.Fatalf("device activity = %#v", device)
		}
	})
}

func TestSessionPoisonsAmbiguousTransportAndStillRetriesClose(t *testing.T) {
	transferFailure := errors.New("write failed")
	releaseFailure := errors.New("release failed")
	device := metadataPeer(t, nil)
	device.writeErr = transferFailure
	device.releaseErr = releaseFailure
	session := &Session{device: device, claim: &peerUSBClaim{device: device}, application: applicationInterface{
		bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 512}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	if _, err := session.exchange(context.Background(), []byte{1}, 0); !errors.Is(err, transferFailure) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("exchange() error = %v", err)
	}
	writes := device.writes
	if _, err := session.exchange(context.Background(), []byte{1}, 0); !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("second exchange() error = %v", err)
	}
	if device.writes != writes {
		t.Fatal("poisoned session issued USB traffic")
	}
	if err := session.Close(); !errors.Is(err, releaseFailure) {
		t.Fatalf("first Close() error = %v", err)
	}
	device.releaseErr = nil
	if err := session.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if device.releases != 2 || device.closes != 1 {
		t.Fatalf("cleanup = releases %d closes %d", device.releases, device.closes)
	}
}

func TestSessionCancellationAbortsAndDrainsAcceptedTransfer(t *testing.T) {
	want := errors.New("transfer aborted")
	transfer := &cancelUSBTransfer{waiting: make(chan struct{}), done: make(chan struct{}), err: want}
	claim := &cancelUSBClaim{transfer: transfer}
	device := metadataPeer(t, nil)
	session := &Session{device: device, claim: claim, application: applicationInterface{
		bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- session.writeExact(ctx, []byte{1}) }()
	<-transfer.waiting
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) || !errors.Is(err, want) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("writeExact() error = %v", err)
	}
	if claim.aborts != 1 || transfer.waits != 2 {
		t.Fatalf("cancellation cleanup = aborts %d waits %d", claim.aborts, transfer.waits)
	}
}

func TestSessionCancellationRetainsTransferAfterFailedAbort(t *testing.T) {
	want := errors.New("abort failed")
	transfer := &cancelUSBTransfer{waiting: make(chan struct{}), done: make(chan struct{})}
	claim := &cancelUSBClaim{transfer: transfer, abortErr: want}
	device := metadataPeer(t, nil)
	session := &Session{device: device, claim: claim, application: applicationInterface{
		bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- session.writeExact(ctx, []byte{1}) }()
	<-transfer.waiting
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) || !errors.Is(err, want) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("writeExact() error = %v", err)
	}
	if claim.aborts != 1 || transfer.waits != 1 {
		t.Fatalf("failed cancellation cleanup = aborts %d waits %d", claim.aborts, transfer.waits)
	}
	select {
	case <-transfer.done:
		t.Fatal("failed abort released the transfer")
	default:
	}
}

func TestSessionCancellationPreservesConcurrentWaitFailure(t *testing.T) {
	waitFailure := errors.New("host engine failed")
	completionFailure := errors.New("transfer aborted")
	ctx, cancel := context.WithCancel(context.Background())
	transfer := &cancelUSBTransfer{done: make(chan struct{}), firstErr: waitFailure, err: completionFailure, cancel: cancel}
	claim := &cancelUSBClaim{transfer: transfer}
	device := metadataPeer(t, nil)
	session := &Session{device: device, claim: claim, application: applicationInterface{
		bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
	}}
	err := session.writeExact(ctx, []byte{1})
	if !errors.Is(err, waitFailure) || !errors.Is(err, context.Canceled) || !errors.Is(err, completionFailure) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("writeExact() error = %v", err)
	}
	if claim.aborts != 1 || transfer.waits != 2 {
		t.Fatalf("cancellation cleanup = aborts %d waits %d", claim.aborts, transfer.waits)
	}
}

func TestSessionCachesDeviceCloseFailureAfterInterfaceRelease(t *testing.T) {
	want := errors.New("device close failed")
	device := metadataPeer(t, nil)
	device.closeErr = want
	session := &Session{device: device, claim: &peerUSBClaim{device: device}}
	if err := session.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() error = %v, want %v", err, want)
	}
	if session.claim != nil || session.device != nil {
		t.Fatalf("session ownership after failed device close = %#v", session)
	}
	device.closeErr = nil
	if err := session.Close(); !errors.Is(err, want) {
		t.Fatalf("second Close() error = %v, want cached %v", err, want)
	}
	if device.releases != 1 || device.closes != 1 {
		t.Fatalf("cleanup = releases %d closes %d device %T", device.releases, device.closes, session.device)
	}
}

func TestOpenHonorsPreCanceledContextWithoutUSBTraffic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	device := metadataPeer(t, nil)
	if _, err := openSession(ctx, device); !errors.Is(err, context.Canceled) {
		t.Fatalf("openSession() error = %v", err)
	}
	if device.configurationN != 0 || device.writes != 0 || device.closes != 1 {
		t.Fatalf("device activity = %#v", device)
	}
}

func metadataPeer(t *testing.T, operations []peerOperation) *peerUSBDevice {
	device := &peerUSBDevice{
		t: t, identity: usb.DeviceInfo{VID: VID, PID: 0x1020, Bus: 1, Address: 2}, operations: operations,
		configuration: usb.Configuration{Value: 1, Interfaces: []usb.Interface{{Number: 3, Alternates: []usb.AlternateSetting{{
			Number: 2, Class: 0xff, Subclass: 0xff, Protocol: 0xff, Endpoints: []usb.Endpoint{
				{Address: 0x84, TransferType: usb.TransferBulk, MaxPacketSize: 512},
				{Address: 0x03, TransferType: usb.TransferBulk, MaxPacketSize: 512},
			},
		}}}}},
	}
	device.endpoints = map[uint8]usb.Endpoint{}
	for _, endpoint := range device.configuration.Interfaces[0].Alternates[0].Endpoints {
		device.endpoints[endpoint.Address] = endpoint
	}
	return device
}
