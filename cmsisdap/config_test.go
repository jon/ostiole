package cmsisdap

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOpenWithSWDOwnsClockAndDisconnectsOnClose(t *testing.T) {
	peer := newSWDCommandPeer(64)
	session, err := openSession(t.Context(), peer.device, WithSWD(4_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if session.MaxClockHz() != 4_000_000 {
		t.Fatalf("SWD clock = %d Hz", session.MaxClockHz())
	}
	if got := peer.effectCommands(); !reflect.DeepEqual(got, [][]byte{{commandConnect, portSWD}, {commandSWJClock, 0x00, 0x09, 0x3d, 0x00}}) {
		t.Fatalf("configuration commands = %x", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := peer.effectCommands(); !reflect.DeepEqual(got, [][]byte{{commandConnect, portSWD}, {commandSWJClock, 0x00, 0x09, 0x3d, 0x00}, {commandDisconnect}}) {
		t.Fatalf("lifecycle commands = %x", got)
	}
	if peer.device.claim.closes != 1 || peer.device.closes != 1 {
		t.Fatalf("close counts = claim %d device %d", peer.device.claim.closes, peer.device.closes)
	}
}

func TestConfigureSWDRejectsInvalidRequestsBeforeEffects(t *testing.T) {
	device := metadataPeer()
	session, err := openSession(t.Context(), device)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]string(nil), device.claim.operations...)
	if err := session.ConfigureSWD(t.Context(), 0); err == nil {
		t.Fatal("ConfigureSWD accepted a zero clock")
	}
	if !reflect.DeepEqual(device.claim.operations, before) {
		t.Fatalf("invalid ConfigureSWD operations = %v", device.claim.operations[len(before):])
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	device = metadataPeer()
	device.claim.responses[infoCapabilities] = infoResponse([]byte{0x10})
	session, err = openSession(t.Context(), device)
	if err != nil {
		t.Fatal(err)
	}
	before = append([]string(nil), device.claim.operations...)
	if err := session.ConfigureSWD(t.Context(), 100_000); err == nil || !strings.Contains(err.Error(), "does not advertise SWD") {
		t.Fatalf("ConfigureSWD without capability error = %v", err)
	}
	if !reflect.DeepEqual(device.claim.operations, before) {
		t.Fatalf("unsupported ConfigureSWD operations = %v", device.claim.operations[len(before):])
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	peer := newSWDCommandPeer(4)
	peer.device.claim.responses[infoProtocolVersion] = infoResponse([]byte("2\x00"))
	peer.device.claim.responses[infoVendor] = infoResponse(nil)
	peer.device.claim.responses[infoFirmwareVersion] = infoResponse(nil)
	session, err = openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	beforeCount := len(peer.commands)
	if err := session.ConfigureSWD(t.Context(), 100_000); err == nil || !strings.Contains(err.Error(), "cannot hold DAP_SWJ_Clock") {
		t.Fatalf("ConfigureSWD with four-byte packets error = %v", err)
	}
	if len(peer.commands) != beforeCount {
		t.Fatal("packet-size rejection sent a target command")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWithSWDRejectsInvalidClockBeforeOpenEffects(t *testing.T) {
	device := metadataPeer()
	if session, err := openSession(t.Context(), device, WithSWD(0)); session != nil || err == nil {
		t.Fatalf("openSession(WithSWD(0)) = (%T, %v)", session, err)
	}
	if len(device.claim.operations) != 0 || device.closes != 1 {
		t.Fatalf("invalid option operations = %v, device closes = %d", device.claim.operations, device.closes)
	}
}

func TestOpenWithSWDRetriesCleanupAfterConfigurationFailure(t *testing.T) {
	peer := newSWDCommandPeer(64)
	peer.clockStatus = statusError
	peer.disconnectStatuses = []byte{statusError, statusOK}
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if session != nil || err == nil || !strings.Contains(err.Error(), "DAP_SWJ_Clock") {
		t.Fatalf("openSession(WithSWD) = (%T, %v)", session, err)
	}
	want := [][]byte{
		{commandConnect, portSWD},
		{commandSWJClock, 0xa0, 0x86, 0x01, 0x00},
		{commandDisconnect},
		{commandDisconnect},
	}
	if got := peer.effectCommands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup commands = %x", got)
	}
	if peer.device.claim.closes != 1 || peer.device.closes != 1 {
		t.Fatalf("close counts = claim %d device %d", peer.device.claim.closes, peer.device.closes)
	}
}

func TestOpenWithSWDRetainsPersistentDisconnectFailure(t *testing.T) {
	peer := newSWDCommandPeer(64)
	peer.clockStatus = statusError
	peer.disconnectStatuses = []byte{statusError, statusError, statusOK}
	session, err := openSession(t.Context(), peer.device, WithSWD(100_000))
	if session == nil || err == nil || !strings.Contains(err.Error(), "retain SWD port") {
		t.Fatalf("openSession(WithSWD) = (%T, %v)", session, err)
	}
	want := [][]byte{
		{commandConnect, portSWD},
		{commandSWJClock, 0xa0, 0x86, 0x01, 0x00},
		{commandDisconnect},
		{commandDisconnect},
	}
	if got := peer.effectCommands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup commands = %x", got)
	}
	if peer.device.claim.closes != 0 || peer.device.closes != 0 {
		t.Fatalf("retained close counts = claim %d device %d", peer.device.claim.closes, peer.device.closes)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if peer.device.claim.closes != 1 || peer.device.closes != 1 {
		t.Fatalf("final close counts = claim %d device %d", peer.device.claim.closes, peer.device.closes)
	}
}

func TestConfigureSWDRejectsProtocolWithoutSequenceBeforeEffects(t *testing.T) {
	device := metadataPeer()
	device.claim.responses[infoProtocolVersion] = infoResponse([]byte("1.1.0\x00"))
	session, err := openSession(t.Context(), device)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]string(nil), device.claim.operations...)
	if err := session.ConfigureSWD(t.Context(), 100_000); err == nil || !strings.Contains(err.Error(), "does not support DAP_SWD_Sequence") {
		t.Fatalf("ConfigureSWD with protocol 1.1 error = %v", err)
	}
	if !reflect.DeepEqual(device.claim.operations, before) {
		t.Fatalf("old-protocol ConfigureSWD operations = %v", device.claim.operations[len(before):])
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureSWDLeavesUnimplementedConnectClean(t *testing.T) {
	peer := newSWDCommandPeer(64)
	peer.connectUnimplemented = true
	session, err := openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	err = session.ConfigureSWD(t.Context(), 100_000)
	if err == nil || !strings.Contains(err.Error(), "not implemented") || errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("ConfigureSWD error = %v", err)
	}
	if session.poisoned {
		t.Fatal("defined unimplemented response poisoned the session")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := peer.effectCommands(); !reflect.DeepEqual(got, [][]byte{{commandConnect, portSWD}}) {
		t.Fatalf("commands = %x", got)
	}
}

func TestConfigureSWDDisconnectsWrongSelectedPort(t *testing.T) {
	peer := newSWDCommandPeer(64)
	peer.connectPort = portJTAG
	session, err := openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ConfigureSWD(t.Context(), 100_000); err == nil || !strings.Contains(err.Error(), "selected port") {
		t.Fatalf("ConfigureSWD error = %v", err)
	}
	if got := peer.effectCommands(); !reflect.DeepEqual(got, [][]byte{{commandConnect, portSWD}, {commandDisconnect}}) {
		t.Fatalf("commands = %x", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureSWDDisconnectsAfterClockFailure(t *testing.T) {
	peer := newSWDCommandPeer(64)
	peer.clockStatus = statusError
	session, err := openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ConfigureSWD(t.Context(), 100_000); err == nil || !strings.Contains(err.Error(), "DAP_SWJ_Clock") {
		t.Fatalf("ConfigureSWD error = %v", err)
	}
	if session.MaxClockHz() != 0 {
		t.Fatalf("failed configuration clock = %d Hz", session.MaxClockHz())
	}
	if got := peer.effectCommands(); !reflect.DeepEqual(got, [][]byte{{commandConnect, portSWD}, {commandSWJClock, 0xa0, 0x86, 0x01, 0x00}, {commandDisconnect}}) {
		t.Fatalf("commands = %x", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRetriesCompleteDisconnectFailure(t *testing.T) {
	peer := newSWDCommandPeer(64)
	peer.disconnectStatuses = []byte{statusError, statusOK}
	session, err := openSession(t.Context(), peer.device)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ConfigureSWD(t.Context(), 100_000); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err == nil || !strings.Contains(err.Error(), "DAP_Disconnect") {
		t.Fatalf("first Close error = %v", err)
	}
	if peer.device.claim.closes != 0 || peer.device.closes != 0 || session.MaxClockHz() == 0 {
		t.Fatalf("first Close released ownership: claim %d device %d clock %d", peer.device.claim.closes, peer.device.closes, session.MaxClockHz())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if peer.device.claim.closes != 1 || peer.device.closes != 1 || session.MaxClockHz() != 0 {
		t.Fatalf("second Close state: claim %d device %d clock %d", peer.device.claim.closes, peer.device.closes, session.MaxClockHz())
	}
}

type swdCommandPeer struct {
	device               *peerUSBDevice
	commands             [][]byte
	connectPort          byte
	clockStatus          byte
	disconnectStatuses   []byte
	connectUnimplemented bool
}

func newSWDCommandPeer(packetSize uint16) *swdCommandPeer {
	device := metadataPeer()
	device.claim.responses[infoPacketSize] = infoResponse([]byte{byte(packetSize), byte(packetSize >> 8)})
	peer := &swdCommandPeer{device: device, connectPort: portSWD}
	device.claim.handle = peer.handle
	return peer
}

func (p *swdCommandPeer) handle(request []byte) ([]byte, error) {
	p.commands = append(p.commands, append([]byte(nil), request...))
	if len(request) == 0 {
		return nil, errors.New("peer: empty command")
	}
	switch request[0] {
	case commandInfo:
		if len(request) != 2 {
			return nil, errors.New("peer: malformed DAP_Info")
		}
		return append([]byte(nil), p.device.claim.responses[request[1]]...), nil
	case commandConnect:
		if !reflect.DeepEqual(request, []byte{commandConnect, portSWD}) {
			return nil, errors.New("peer: malformed DAP_Connect")
		}
		if p.connectUnimplemented {
			return []byte{statusError}, nil
		}
		return []byte{commandConnect, p.connectPort}, nil
	case commandSWJClock:
		if len(request) != 5 || binary.LittleEndian.Uint32(request[1:]) == 0 {
			return nil, errors.New("peer: malformed DAP_SWJ_Clock")
		}
		return []byte{commandSWJClock, p.clockStatus}, nil
	case commandDisconnect:
		status := byte(statusOK)
		if len(p.disconnectStatuses) != 0 {
			status = p.disconnectStatuses[0]
			p.disconnectStatuses = p.disconnectStatuses[1:]
		}
		return []byte{commandDisconnect, status}, nil
	default:
		return nil, errors.New("peer: unknown command")
	}
}

func (p *swdCommandPeer) effectCommands() [][]byte {
	var result [][]byte
	for _, command := range p.commands {
		if command[0] != commandInfo {
			result = append(result, command)
		}
	}
	return result
}
