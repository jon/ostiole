package main

import (
	"reflect"
	"testing"
)

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
