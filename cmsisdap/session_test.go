package cmsisdap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jon/ostiole/usb"
)

type peerUSBDevice struct {
	identity      usb.DeviceInfo
	configuration usb.Configuration
	configErr     error
	claim         *peerUSBClaim
	claimErr      error
	closes        int
	closeErrs     []error
}

func (d *peerUSBDevice) Identity() usb.DeviceInfo { return d.identity }

func (d *peerUSBDevice) ActiveConfiguration(context.Context) (usb.Configuration, error) {
	if d.configErr != nil {
		return usb.Configuration{}, d.configErr
	}
	return d.configuration, nil
}

func (d *peerUSBDevice) claimInterface(number uint8) (usbClaim, error) {
	if d.claimErr != nil {
		return nil, d.claimErr
	}
	d.claim.operations = append(d.claim.operations, fmt.Sprintf("claim %d", number))
	return d.claim, nil
}

func (d *peerUSBDevice) Close() error {
	d.closes++
	if len(d.closeErrs) != 0 {
		err := d.closeErrs[0]
		d.closeErrs = d.closeErrs[1:]
		return err
	}
	return nil
}

type peerUSBClaim struct {
	endpoints    map[uint8]usb.Endpoint
	responses    map[byte][]byte
	handle       func([]byte) ([]byte, error)
	operations   []string
	pendingIn    *peerBulkTransfer
	inputErr     error
	alternateErr error
	endpointErrs map[uint8]error
	closes       int
	closeErrs    []error
}

func (c *peerUSBClaim) SetAltSetting(alternate uint8) error {
	c.operations = append(c.operations, fmt.Sprintf("alternate %d", alternate))
	if c.alternateErr != nil {
		return c.alternateErr
	}
	return nil
}

func (c *peerUSBClaim) Endpoint(_ context.Context, address uint8) (usb.Endpoint, error) {
	c.operations = append(c.operations, fmt.Sprintf("endpoint %#02x", address))
	if err := c.endpointErrs[address]; err != nil {
		return usb.Endpoint{}, err
	}
	endpoint, ok := c.endpoints[address]
	if !ok {
		return usb.Endpoint{}, errors.New("missing endpoint")
	}
	return endpoint, nil
}

func (c *peerUSBClaim) SubmitBulk(_ context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	if endpoint&0x80 != 0 {
		c.operations = append(c.operations, "submit IN")
		transfer := &peerBulkTransfer{buffer: buffer, err: c.inputErr}
		c.inputErr = nil
		c.pendingIn = transfer
		return transfer, nil
	}
	c.operations = append(c.operations, fmt.Sprintf("submit OUT %x", buffer))
	if c.pendingIn == nil {
		return nil, errors.New("OUT submitted without a pending IN request")
	}
	var response []byte
	if c.handle != nil {
		var err error
		response, err = c.handle(append([]byte(nil), buffer...))
		if err != nil {
			return nil, err
		}
	} else {
		var ok bool
		response, ok = c.responses[buffer[1]]
		if !ok || len(buffer) != 2 || buffer[0] != commandInfo {
			return nil, fmt.Errorf("unexpected command %x", buffer)
		}
	}
	count := copy(c.pendingIn.buffer, response)
	c.pendingIn.count = count
	c.pendingIn = nil
	return &peerBulkTransfer{count: len(buffer)}, nil
}

func (c *peerUSBClaim) AbortBulk(endpoint uint8) error {
	c.operations = append(c.operations, fmt.Sprintf("abort %#02x", endpoint))
	return nil
}

func (c *peerUSBClaim) Close() error {
	c.closes++
	if len(c.closeErrs) != 0 {
		err := c.closeErrs[0]
		c.closeErrs = c.closeErrs[1:]
		return err
	}
	return nil
}

type peerBulkTransfer struct {
	buffer []byte
	count  int
	err    error
}

func (t *peerBulkTransfer) Wait(context.Context) (int, error) { return t.count, t.err }

func TestOpenReadsMetadataWithoutConnecting(t *testing.T) {
	device := metadataPeer()
	claim := device.claim

	session, err := openSession(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionInfo(t, session.Info(), device.identity)
	assertMetadataOperations(t, claim.operations)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if claim.closes != 1 || device.closes != 1 {
		t.Fatalf("close counts = claim %d device %d", claim.closes, device.closes)
	}
}

func TestOpenRejectsMalformedMetadata(t *testing.T) {
	tests := []struct {
		name     string
		id       byte
		response []byte
		text     string
	}{
		{name: "unimplemented", id: infoPacketSize, response: []byte{0xff}, text: "command is not implemented"},
		{name: "short response", id: infoPacketSize, response: []byte{commandInfo}, text: "short response"},
		{name: "wrong command", id: infoPacketSize, response: []byte{0x01, 0}, text: "response command"},
		{name: "declared length exceeds packet", id: infoPacketSize, response: []byte{commandInfo, 63}, text: "declared length 63 exceeds"},
		{name: "short declared data", id: infoPacketSize, response: []byte{commandInfo, 2, 1}, text: "got 3 bytes, need 4"},
		{name: "packet size length", id: infoPacketSize, response: infoResponse([]byte{64}), text: "packet-size length = 1"},
		{name: "packet size too small", id: infoPacketSize, response: infoResponse([]byte{3, 0}), text: "packet size = 3"},
		{name: "packet count length", id: infoPacketCount, response: infoResponse([]byte{1, 2}), text: "invalid DAP_Info packet count"},
		{name: "packet count zero", id: infoPacketCount, response: infoResponse([]byte{0}), text: "invalid DAP_Info packet count"},
		{name: "capabilities absent", id: infoCapabilities, response: infoResponse(nil), text: "capabilities length = 0"},
		{name: "capabilities too long", id: infoCapabilities, response: infoResponse([]byte{1, 2, 3}), text: "capabilities length = 3"},
		{name: "protocol absent", id: infoProtocolVersion, response: infoResponse(nil), text: "no protocol version"},
		{name: "protocol missing NUL", id: infoProtocolVersion, response: infoResponse([]byte("2.1")), text: "not NUL-terminated"},
		{name: "protocol embedded NUL", id: infoProtocolVersion, response: infoResponse([]byte{'2', 0, '1', 0}), text: "embedded NUL"},
		{name: "protocol invalid UTF-8", id: infoProtocolVersion, response: infoResponse([]byte{0xff, 0}), text: "not UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			device := metadataPeer()
			device.claim.responses[test.id] = test.response
			session, err := openSession(context.Background(), device)
			if session != nil || err == nil || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("openSession() = (%T, %v), want error containing %q", session, err, test.text)
			}
			if device.claim.closes != 1 || device.closes != 1 {
				t.Fatalf("close counts = claim %d device %d", device.claim.closes, device.closes)
			}
		})
	}
}

func assertSessionInfo(t *testing.T, info Info, identity usb.DeviceInfo) {
	t.Helper()
	if info.USB != identity || info.Vendor != "Acme" || info.Product != identity.Product || info.Serial != identity.Serial ||
		info.ProtocolVersion != "2.1.2" || info.FirmwareVersion != "2026.08" || info.PacketSize != 512 || info.PacketCount != 2 {
		t.Fatalf("Info() = %#v", info)
	}
	if !info.Capabilities.Has(CapabilitySWD) || !info.Capabilities.Has(CapabilityAtomicCommands) ||
		!info.Capabilities.Has(CapabilityUSBComPort) || info.Capabilities.Has(CapabilityJTAG) {
		t.Fatalf("Capabilities = %x", info.Capabilities.Bytes())
	}
	if got := info.Capabilities.Bytes(); !reflect.DeepEqual(got, []byte{0x11, 0x01}) {
		t.Fatalf("capability bytes = %x", got)
	}
}

func assertMetadataOperations(t *testing.T, operations []string) {
	t.Helper()
	for index, operation := range operations {
		if operation == "submit IN" {
			if index+1 >= len(operations) || !strings.HasPrefix(operations[index+1], "submit OUT") {
				t.Fatalf("operations = %v", operations)
			}
		}
	}
	for _, operation := range operations {
		if operation == "submit OUT 0201" {
			t.Fatalf("metadata-only Open sent DAP_Connect: %v", operations)
		}
	}
}

func TestOpenRejectsDeviceWithoutV2Interface(t *testing.T) {
	device := &peerUSBDevice{
		identity: usb.DeviceInfo{Product: "DAPLink CMSIS-DAP"},
		configuration: usb.Configuration{Interfaces: []usb.Interface{{
			Number: 3, Alternates: []usb.AlternateSetting{{Class: 0x03}},
		}}},
		claim: &peerUSBClaim{},
	}

	session, err := openSession(context.Background(), device)
	if session != nil || !errors.Is(err, ErrNoV2Interface) {
		t.Fatalf("openSession() = (%T, %v), want nil/ErrNoV2Interface", session, err)
	}
	if device.closes != 1 {
		t.Fatalf("device closes = %d, want 1", device.closes)
	}
}

func TestOpenAttemptsCleanupAfterEachFailureBoundary(t *testing.T) {
	want := errors.New("open failed")
	tests := []struct {
		name        string
		ctx         func() context.Context
		configure   func(*peerUSBDevice)
		text        string
		claimCloses int
	}{
		{name: "nil context", ctx: func() context.Context { return nil }, text: "nil open context"},
		{
			name: "canceled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			text: "context canceled",
		},
		{
			name: "configuration inspection",
			configure: func(device *peerUSBDevice) {
				device.configErr = want
			},
			text: "inspect active USB configuration",
		},
		{
			name: "interface claim",
			configure: func(device *peerUSBDevice) {
				device.claimErr = want
			},
			text: "claim USB interface",
		},
		{
			name: "alternate selection",
			configure: func(device *peerUSBDevice) {
				device.claim.alternateErr = want
			},
			text:        "select USB interface",
			claimCloses: 1,
		},
		{
			name: "endpoint lookup",
			configure: func(device *peerUSBDevice) {
				device.claim.endpointErrs = map[uint8]error{0x01: want}
			},
			text:        "resolve active bulk endpoint",
			claimCloses: 1,
		},
		{
			name: "active endpoint validation",
			configure: func(device *peerUSBDevice) {
				device.claim.endpoints[0x01] = usb.Endpoint{Address: 0x02, TransferType: usb.TransferBulk, MaxPacketSize: 64}
			},
			text:        "invalid active bulk endpoint",
			claimCloses: 1,
		},
		{
			name: "bootstrap packet size",
			configure: func(device *peerUSBDevice) {
				device.claim.endpoints[0x82] = usb.Endpoint{Address: 0x82, TransferType: usb.TransferBulk, MaxPacketSize: 2}
			},
			text:        "want at least 4",
			claimCloses: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			device := metadataPeer()
			if test.configure != nil {
				test.configure(device)
			}
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			session, err := openSession(ctx, device)
			if session != nil || err == nil || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("openSession() = (%T, %v), want error containing %q", session, err, test.text)
			}
			if device.claim.closes != test.claimCloses || device.closes != 1 {
				t.Fatalf("close counts = claim %d device %d, want %d/1", device.claim.closes, device.closes, test.claimCloses)
			}
		})
	}
}

func TestInfoReturnsDetachedCapabilities(t *testing.T) {
	session := &Session{info: Info{Capabilities: Capabilities{bytes: []byte{0x03}}}}
	info := session.Info()
	info.Capabilities.bytes[0] = 0
	if got := session.Info().Capabilities.Bytes(); !reflect.DeepEqual(got, []byte{0x03}) {
		t.Fatalf("Info().Capabilities = %x", got)
	}
}

func TestCloseRetriesClaimReleaseAndCachesDeviceClose(t *testing.T) {
	releaseFailure := errors.New("release failed")
	closeFailure := errors.New("close failed")
	claim := &peerUSBClaim{closeErrs: []error{releaseFailure}}
	device := &peerUSBDevice{claim: claim, closeErrs: []error{closeFailure}}
	session := &Session{device: device, claim: claim}

	if err := session.Close(); !errors.Is(err, releaseFailure) {
		t.Fatalf("first Close() error = %v", err)
	}
	if claim.closes != 1 || device.closes != 0 {
		t.Fatalf("first close counts = claim %d device %d", claim.closes, device.closes)
	}
	if err := session.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("second Close() error = %v", err)
	}
	if claim.closes != 2 || device.closes != 1 {
		t.Fatalf("second close counts = claim %d device %d", claim.closes, device.closes)
	}
	if err := session.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("third Close() error = %v", err)
	}
	if claim.closes != 2 || device.closes != 1 {
		t.Fatalf("third close counts = claim %d device %d", claim.closes, device.closes)
	}
}

func infoResponse(data []byte) []byte {
	return append([]byte{commandInfo, byte(len(data))}, data...)
}

func metadataPeer() *peerUSBDevice {
	out := usb.Endpoint{Address: 0x01, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	in := usb.Endpoint{Address: 0x82, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	claim := &peerUSBClaim{
		endpoints: map[uint8]usb.Endpoint{out.Address: out, in.Address: in},
		responses: map[byte][]byte{
			infoPacketSize:      infoResponse([]byte{0x00, 0x02}),
			infoPacketCount:     infoResponse([]byte{2}),
			infoCapabilities:    infoResponse([]byte{0x11, 0x01}),
			infoProtocolVersion: infoResponse([]byte("2.1.2\x00")),
			infoVendor:          infoResponse([]byte("Acme\x00")),
			infoProduct:         infoResponse(nil),
			infoSerial:          infoResponse(nil),
			infoFirmwareVersion: infoResponse([]byte("2026.08\x00")),
		},
	}
	return &peerUSBDevice{
		identity: usb.DeviceInfo{VID: 0x1234, PID: 0x5678, Bus: 1, Address: 2, Product: "Acme CMSIS-DAP", Serial: "USB123"},
		configuration: usb.Configuration{Value: 1, Interfaces: []usb.Interface{{
			Number: 3, Alternates: []usb.AlternateSetting{{Class: 0xff, Endpoints: []usb.Endpoint{out, in}}},
		}}},
		claim: claim,
	}
}
