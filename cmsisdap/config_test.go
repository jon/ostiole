package cmsisdap

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
)

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
	if session.configured || session.maxClockHz != 0 {
		t.Fatalf("failed configuration state = configured %t clock %d Hz", session.configured, session.maxClockHz)
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
	if peer.device.claim.closes != 0 || peer.device.closes != 0 || !session.configured {
		t.Fatalf("first Close released ownership: claim %d device %d configured %t", peer.device.claim.closes, peer.device.closes, session.configured)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if peer.device.claim.closes != 1 || peer.device.closes != 1 || session.configured {
		t.Fatalf("second Close state: claim %d device %d configured %t", peer.device.claim.closes, peer.device.closes, session.configured)
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
