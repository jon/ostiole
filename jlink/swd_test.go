package jlink

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

var _ swd.Wire = (*Session)(nil)
var _ swd.TransferLimits = (*Session)(nil)

func TestOpenWithSWDSelectsInterfaceClockAndConservativeLimit(t *testing.T) {
	device := metadataPeer(t, configuredSWDOperations(delayedInputFirmwareRecord, 100))
	device.endpoints[0x84] = usb.Endpoint{Address: 0x84, TransferType: usb.TransferBulk, MaxPacketSize: 64}
	session, err := openSession(context.Background(), device, WithSWD(100_999))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	}()
	if session.ClockHz() != 100_000 || session.MaxTransferBits() != 504 {
		t.Fatalf("SWD configuration = %d Hz, %d bits", session.ClockHz(), session.MaxTransferBits())
	}
	if session.Info().SelectedInterface != interfaceSWD || len(device.operations) != 0 {
		t.Fatalf("session info = %#v, operations left %d", session.Info(), len(device.operations))
	}
}

func TestOpenOptionsApplyInOrderAndRejectInvalidClockBeforeTraffic(t *testing.T) {
	t.Run("last SWD option wins", func(t *testing.T) {
		device := metadataPeer(t, configuredSWDOperations("another probe", 100))
		session, err := openSession(context.Background(), device, WithSWD(200_000), Option{}, WithSWD(100_000))
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("invalid clock", func(t *testing.T) {
		device := metadataPeer(t, nil)
		if _, err := openSession(context.Background(), device, WithSWD(999)); err == nil {
			t.Fatal("openSession() succeeded")
		}
		if device.configurationN != 0 || device.claimed != 0 || device.writes != 0 || device.closes != 1 {
			t.Fatalf("device activity = %#v", device)
		}
	})
}

func TestConfigureSWDRequiresAdvertisedInterfaceBeforeTraffic(t *testing.T) {
	device := metadataPeer(t, nil)
	session := &Session{device: device, application: applicationInterface{
		bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 64}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 64},
	}}
	if err := session.ConfigureSWD(context.Background(), 100_000); err == nil {
		t.Fatal("ConfigureSWD() succeeded")
	}
	if device.writes != 0 {
		t.Fatalf("bulk writes = %d, want 0", device.writes)
	}
}

func TestConfigureSWDRejectsPreviousInterfaceOutsideAvailabilityMask(t *testing.T) {
	device := metadataPeer(t, []peerOperation{
		{request: []byte{commandInterface, interfaceSWD}, response: [][]byte{{32, 0, 0, 0}}},
		{request: []byte{commandClockSet, 100, 0}},
	})
	session := configuredSession(device, "another probe")
	session.configured = false
	session.info.SelectedInterfaceKnown = true
	session.info.AvailableInterfaces = 1 << interfaceSWD
	err := session.ConfigureSWD(context.Background(), 100_000)
	if err == nil {
		t.Fatal("ConfigureSWD() succeeded")
	}
	if session.configured || device.writes != 1 || len(device.operations) != 1 {
		t.Fatalf("configured = %t, writes = %d, operations left = %d", session.configured, device.writes, len(device.operations))
	}
}

func TestConfigureSWDRejectsInsufficientWorkspaceBeforeTraffic(t *testing.T) {
	device := metadataPeer(t, nil)
	session := configuredSession(device, "another probe")
	session.info.SelectedInterfaceKnown = true
	session.info.AvailableInterfaces = 1 << interfaceSWD
	session.info.WorkspaceKnown, session.info.Workspace = true, 36
	err := session.ConfigureSWD(context.Background(), 100_000)
	if err == nil || !strings.Contains(err.Error(), "transfer limit") {
		t.Fatalf("ConfigureSWD() error = %v", err)
	}
	if device.writes != 0 || !session.configured || session.ClockHz() != 100_000 || session.MaxTransferBits() != 504 {
		t.Fatalf("traffic = %d, configuration = %t %d Hz %d bits", device.writes, session.configured, session.ClockHz(), session.MaxTransferBits())
	}
}

func TestSWDIOFramesDirectionAndZeroesUndrivenOutput(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{
		request: []byte{0xcf, 0, 9, 0, 0x07, 0, 0x05, 0}, response: [][]byte{{0xaa, 0xff}, {0}},
	}})
	session := configuredSession(device, "another probe")
	got, err := session.SWDIO(context.Background(), []byte{0x07, 0}, []byte{0xfd, 0xff}, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []byte{0xaa, 0x01}) {
		t.Fatalf("SWDIO() = %x, want aa01", got)
	}
}

func TestSWDIOConsumesCoalescedPacketBoundaryStatus(t *testing.T) {
	const bits = 504
	request := make([]byte, 4+2*(bits/8))
	request[0] = commandScanV3
	binary.LittleEndian.PutUint16(request[2:4], bits)
	samples := bytes.Repeat([]byte{0xa5}, bits/8)
	response := append(append([]byte(nil), samples...), 0)
	device := metadataPeer(t, []peerOperation{{request: request, response: [][]byte{response}}})
	session := configuredSession(device, "another probe")
	session.application.bulkIn.MaxPacketSize = 64
	got, err := session.SWDIO(context.Background(), make([]byte, bits/8), make([]byte, bits/8), bits)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, samples) || device.reads != 1 {
		t.Fatalf("SWDIO() = %x after %d reads", got, device.reads)
	}
}

func TestSWDIOPoisonsTrailingResponseBytes(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{
		request: []byte{0xcf, 0, 1, 0, 0, 0}, response: [][]byte{{0, 0, 0xff}},
	}})
	session := configuredSession(device, "another probe")
	_, err := session.SWDIO(context.Background(), []byte{0}, []byte{0}, 1)
	if !errors.Is(err, ErrSessionPoisoned) || !strings.Contains(err.Error(), "read SWD scan status") || !strings.Contains(err.Error(), "unexpected 1-byte remainder") {
		t.Fatalf("SWDIO() error = %v", err)
	}
	if _, err := session.SWDIO(context.Background(), []byte{0}, []byte{0}, 1); !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("SWDIO() after surplus error = %v", err)
	}
	if device.writes != 1 || device.reads != 1 {
		t.Fatalf("transfers after surplus = %d writes, %d reads", device.writes, device.reads)
	}
}

func TestSWDIOCarriesObservedEDUMiniInputDelayAcrossCalls(t *testing.T) {
	device := metadataPeer(t, []peerOperation{
		{request: []byte{0xcf, 0, 4, 0, 0, 0}, response: [][]byte{{0x0d}, {0}}},
		{request: []byte{0xcf, 0, 3, 0, 0, 0}, response: [][]byte{{0x02}, {0}}},
	})
	session := configuredSession(device, delayedInputFirmwareRecord)
	first, err := session.SWDIO(context.Background(), []byte{0}, []byte{0}, 4)
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.SWDIO(context.Background(), []byte{0}, []byte{0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, []byte{0x0a}) || !reflect.DeepEqual(second, []byte{0x05}) {
		t.Fatalf("delayed samples = %x then %x, want 0a then 05", first, second)
	}
}

func TestSWDIOLeavesUnrecognizedFirmwareSamplesUnchanged(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{
		request: []byte{0xcf, 0, 4, 0, 0, 0}, response: [][]byte{{0x0d}, {0}},
	}})
	record := delayedInputFirmwareRecord[:len(delayedInputFirmwareRecord)-1] + "\x01"
	session := configuredSession(device, record)
	if session.info.Firmware != delayedInputFirmware {
		t.Fatalf("firmware display = %q, want %q", session.info.Firmware, delayedInputFirmware)
	}
	got, err := session.SWDIO(context.Background(), []byte{0}, []byte{0}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []byte{0x0d}) {
		t.Fatalf("SWDIO() = %x, want 0d", got)
	}
}

func TestSWDIOAttributesTransportFailurePhase(t *testing.T) {
	want := errors.New("USB failed")
	for _, test := range []struct {
		name, phase string
		prepare     func(*peerUSBDevice)
	}{
		{name: "command", phase: "write SWD scan command", prepare: func(device *peerUSBDevice) { device.writeErr = want }},
		{name: "samples", phase: "read SWD scan samples", prepare: func(device *peerUSBDevice) { device.readErrs = []error{want} }},
		{name: "status", phase: "read SWD scan status", prepare: func(device *peerUSBDevice) { device.readErrs = []error{nil, want} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			device := metadataPeer(t, []peerOperation{{request: []byte{0xcf, 0, 1, 0, 0, 0}, response: [][]byte{{0}, {0}}}})
			test.prepare(device)
			session := configuredSession(device, "another probe")
			_, err := session.SWDIO(context.Background(), []byte{0}, []byte{0}, 1)
			if !errors.Is(err, want) || !errors.Is(err, ErrSessionPoisoned) || !strings.Contains(err.Error(), test.phase) {
				t.Fatalf("SWDIO() error = %v", err)
			}
		})
	}
}

func TestSWDIOReportsCompleteProbeStatusWithoutPoisoningSession(t *testing.T) {
	clock := make([]byte, 6)
	binary.LittleEndian.PutUint32(clock, 96_000_000)
	binary.LittleEndian.PutUint16(clock[4:], 24)
	device := metadataPeer(t, []peerOperation{
		{request: []byte{0xcf, 0, 1, 0, 0, 0}, response: [][]byte{{0}, {6}}},
		{request: []byte{0xc7, 0x01}, response: [][]byte{{1, 0, 0, 0}}},
		{request: []byte{0xc0}, response: [][]byte{clock}},
		{request: []byte{0x05, 100, 0}},
	})
	session := configuredSession(device, "another probe")
	session.info.SelectedInterfaceKnown = true
	session.info.AvailableInterfaces = 1 << interfaceSWD
	session.info.Capabilities = Capabilities{bytes: []byte{0, 1 << (capabilityClock - 8)}}
	_, err := session.SWDIO(context.Background(), []byte{0}, []byte{0}, 1)
	var scanError *ScanError
	if !errors.As(err, &scanError) || scanError.Status != 6 {
		t.Fatalf("SWDIO() error = %v", err)
	}
	if errors.Is(err, ErrSessionPoisoned) || session.poisoned || session.configured {
		t.Fatalf("session after status = poisoned %t configured %t", session.poisoned, session.configured)
	}
	if err := session.ConfigureSWD(context.Background(), 100_000); err != nil {
		t.Fatalf("ConfigureSWD() after status: %v", err)
	}
	if !session.configured || len(device.operations) != 0 {
		t.Fatalf("reconfigured = %t, operations left %d", session.configured, len(device.operations))
	}
}

func TestCanceledResponsePoisonsOutstandingOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	device := metadataPeer(t, []peerOperation{{request: []byte{1}, response: [][]byte{{0}}}})
	device.afterWrite = cancel
	session := configuredSession(device, "another probe")
	if _, err := session.exchange(ctx, []byte{1}, 1); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSessionPoisoned) {
		t.Fatalf("exchange() error = %v", err)
	}
}

func TestSWDTransferLimitUsesWorkspaceWithoutApplyingUSBPacketPolicy(t *testing.T) {
	device := metadataPeer(t, nil)
	session := configuredSession(device, "another probe")
	session.info.WorkspaceKnown, session.info.Workspace = true, 36
	if _, err := session.swdTransferLimit(); err == nil {
		t.Fatal("swdTransferLimit() accepted less than one connection sequence")
	}
	for _, test := range []struct {
		workspace uint32
		want      int
	}{
		{workspace: 38, want: 136},
		{workspace: 132, want: 504},
		{workspace: ^uint32(0), want: 504},
	} {
		session.info.Workspace = test.workspace
		limit, err := session.swdTransferLimit()
		if err != nil {
			t.Fatalf("workspace %d: %v", test.workspace, err)
		}
		if limit != test.want {
			t.Fatalf("workspace %d limit = %d bits, want %d", test.workspace, limit, test.want)
		}
	}
	session.application.bulkIn.MaxPacketSize = 64
	limit, err := session.swdTransferLimit()
	if err != nil {
		t.Fatal(err)
	}
	if limit != 504 {
		t.Fatalf("packet-independent bits = %d, want 504", limit)
	}
}

func configuredSWDOperations(record string, rateKHz uint16) []peerOperation {
	firmwareRecord := []byte(record)
	firmwareResponses := [][]byte{firmwareRecord}
	if len(firmwareRecord) > 64 {
		firmwareResponses = [][]byte{firmwareRecord[:64], firmwareRecord[64:]}
	}
	capabilities := []byte{0, 0x0a, 0x02, 0}
	workspace := make([]byte, 4)
	binary.LittleEndian.PutUint32(workspace, 11_608)
	clock := make([]byte, 6)
	binary.LittleEndian.PutUint32(clock, 96_000_000)
	binary.LittleEndian.PutUint16(clock[4:], 24)
	return []peerOperation{
		{request: []byte{0x01}, response: append([][]byte{{byte(len(firmwareRecord)), 0}}, firmwareResponses...)},
		{request: []byte{0xe8}, response: [][]byte{capabilities}},
		{request: []byte{0xd4}, response: [][]byte{workspace}},
		{request: []byte{0xc7, 0xff}, response: [][]byte{{0x83, 0, 0, 0}}},
		{request: []byte{0xc7, 0xfe}, response: [][]byte{{0, 0, 0, 0}}},
		{request: []byte{0xc7, 0x01}, response: [][]byte{{0, 0, 0, 0}}},
		{request: []byte{0xc0}, response: [][]byte{clock}},
		{request: []byte{0x05, byte(rateKHz), byte(rateKHz >> 8)}},
	}
}

func configuredSession(device *peerUSBDevice, record string) *Session {
	firmware := record
	if end := strings.IndexByte(firmware, 0); end >= 0 {
		firmware = firmware[:end]
	}
	return &Session{
		device: device, claim: &peerUSBClaim{device: device}, application: applicationInterface{
			bulkIn: usb.Endpoint{Address: 0x84, MaxPacketSize: 512}, bulkOut: usb.Endpoint{Address: 0x03, MaxPacketSize: 512},
		},
		info: Info{USB: device.identity, Firmware: firmware, FirmwareRecord: []byte(record)}, configured: true, clockHz: 100_000, transferBits: 504,
		delayInput: device.identity.PID == 0x1020 && record == delayedInputFirmwareRecord,
	}
}
