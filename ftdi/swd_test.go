package ftdi

import (
	"context"
	"reflect"
	"testing"
)

func TestChannelExecutesDirectionSafeSWDRuns(t *testing.T) {
	raw := &fakeUSBDevice{
		readData: [][]byte{{0x01, 0x60, 0b10100000}},
	}
	channel, err := newChannel(raw, Config{
		ProductID: PIDFT232H,
		Port:      PortA,
		Interface: SWD,
	})
	if err != nil {
		t.Fatal(err)
	}

	input, err := channel.SWDIO(
		context.Background(),
		[]byte{0b00000011},
		[]byte{0b00000001},
		5,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantCommands := []byte{
		0x80, 0x02, 0x03,
		0x1b, 1, 1,
		0x80, 0, 0x01,
		0x2a, 2,
		0x80, 0, 0x01,
		0x87,
	}
	if len(raw.writes) != 1 ||
		!reflect.DeepEqual(raw.writes[0], wantCommands) {
		t.Fatalf("commands = % x, want % x", raw.writes, wantCommands)
	}
	if !reflect.DeepEqual(input, []byte{0b00010100}) {
		t.Fatalf("input = %08b", input)
	}
}
