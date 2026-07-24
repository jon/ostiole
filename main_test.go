package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListAttachmentsFindsSupportedFTDIWithoutStrings(t *testing.T) {
	root := t.TempDir()
	writeAttachment(t, root, "1-2", "0403", "6014", "1", "7")
	writeAttachment(t, root, "2-1", "1234", "5678", "2", "3")

	got, err := listAttachments(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []attachment{{pid: ft232hPID, bus: 1, address: 7}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments = %#v, want %#v", got, want)
	}
}

func TestSWDCommandsTriStateOutputWhileSampling(t *testing.T) {
	direction := []byte{0b00000011}
	output := []byte{0b00000001}
	commands, reads := swdCommands(direction, output, 5)

	if len(reads) != 1 || reads[0] != (readChunk{offset: 2, bits: 3}) {
		t.Fatalf("reads = %#v", reads)
	}
	want := []byte{
		cmdSetDataLow, pinDataOut, pinClock | pinDataOut,
		cmdClockBitsOutNegLSB, 1, 1,
		cmdSetDataLow, 0, pinClock,
		cmdClockBitsInPosLSB, 2,
		cmdSetDataLow, 0, pinClock,
		cmdSendImmediate,
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = % x, want % x", commands, want)
	}
}

func TestDecodeSamplesRestoresLSBFirstBits(t *testing.T) {
	got := decodeSamples(
		[]byte{0b10100000},
		[]readChunk{{offset: 2, bits: 3}},
		5,
	)
	if !reflect.DeepEqual(got, []byte{0b00010100}) {
		t.Fatalf("samples = %08b", got)
	}
}

func TestAppendFTDIPacketIgnoresStatusOnlyPacket(t *testing.T) {
	var payload []byte
	var err error
	payload, err = appendFTDIPacket(payload, []byte{0x32, 0x60}, 2)
	if err != nil {
		t.Fatal(err)
	}
	payload, err = appendFTDIPacket(
		payload,
		[]byte{0x32, 0x60, 0xfa, 0xab},
		4,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload, []byte{0xfa, 0xab}) {
		t.Fatalf("payload = % x", payload)
	}
}

func writeAttachment(
	t *testing.T,
	root, name, vendor, product, bus, address string,
) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for filename, value := range map[string]string{
		"idVendor":  vendor,
		"idProduct": product,
		"busnum":    bus,
		"devnum":    address,
	} {
		if err := os.WriteFile(
			filepath.Join(path, filename),
			[]byte(value+"\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
}
