package ftdi

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestChannelExecutesDirectionSafeSWDRuns(t *testing.T) {
	raw := &fakeUSBDevice{
		readData: [][]byte{{0x01, 0x60, 0b10100000}},
	}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, channel)

	input, err := channel.SWDIO(context.Background(), []byte{0b00000011}, []byte{0b00000001}, 5)
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

func TestChannelReportsSWDTransferLimit(t *testing.T) {
	if got := (*Channel)(nil).MaxTransferBits(); got != 16_384 {
		t.Fatalf("MaxTransferBits() = %d, want 16384", got)
	}
}

func TestChannelPoisonsAfterAmbiguousBulkWrite(t *testing.T) {
	tests := []struct {
		name string
		raw  *fakeUSBDevice
		want error
	}{
		{name: "transfer error", raw: &fakeUSBDevice{writeErr: 1}, want: errors.New("injected write failure")},
		{name: "no progress", raw: &fakeUSBDevice{writeN: []int{0}}, want: io.ErrNoProgress},
		{name: "invalid count", raw: &fakeUSBDevice{writeN: []int{1_000}}, want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel, err := newChannel(test.raw, Config{Port: PortA, MaxClockHz: 400_000})
			if err != nil {
				t.Fatal(err)
			}
			claimFakeChannel(t, channel)

			_, err = channel.SWDIO(context.Background(), []byte{1}, []byte{1}, 1)
			if !errors.Is(err, ErrChannelPoisoned) {
				t.Fatalf("SWDIO() error = %v, want ErrChannelPoisoned", err)
			}
			if test.want != nil && !errors.Is(err, test.want) && !strings.Contains(err.Error(), test.want.Error()) {
				t.Fatalf("SWDIO() error = %v, want original error %v", err, test.want)
			}
			_, err = channel.SWDIO(context.Background(), []byte{1}, []byte{1}, 1)
			if !errors.Is(err, ErrChannelPoisoned) {
				t.Fatalf("second SWDIO() error = %v, want ErrChannelPoisoned", err)
			}
			if test.raw.writesN != 1 {
				t.Fatalf("bulk writes = %d, want 1", test.raw.writesN)
			}
		})
	}
}

func TestChannelPoisonsAfterAmbiguousBulkRead(t *testing.T) {
	readFailure := errors.New("injected read failure")
	tests := []struct {
		name string
		raw  *fakeUSBDevice
	}{
		{name: "transfer error", raw: &fakeUSBDevice{readErr: readFailure}},
		{name: "invalid count", raw: &fakeUSBDevice{readData: [][]byte{{0x01}}}},
		{name: "surplus payload", raw: &fakeUSBDevice{readData: [][]byte{{0x01, 0x60, 0xaa, 0xbb}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel, err := newChannel(test.raw, Config{Port: PortA, MaxClockHz: 400_000})
			if err != nil {
				t.Fatal(err)
			}
			claimFakeChannel(t, channel)

			_, err = channel.SWDIO(context.Background(), []byte{0}, []byte{0}, 1)
			if !errors.Is(err, ErrChannelPoisoned) {
				t.Fatalf("SWDIO() error = %v, want ErrChannelPoisoned", err)
			}
			writes, reads := test.raw.writesN, test.raw.readsN
			_, err = channel.SWDIO(context.Background(), []byte{0}, []byte{0}, 1)
			if !errors.Is(err, ErrChannelPoisoned) {
				t.Fatalf("second SWDIO() error = %v, want ErrChannelPoisoned", err)
			}
			if test.raw.writesN != writes || test.raw.readsN != reads {
				t.Fatalf("bulk calls after poison = writes %d, reads %d", test.raw.writesN-writes, test.raw.readsN-reads)
			}
		})
	}
}

func TestChannelRejectsWriteOnlySWDIOAfterAsyncReceiveFailure(t *testing.T) {
	want := errors.New("injected asynchronous read failure")
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, channel)
	if !raw.failFirstRead(want) {
		t.Fatal("no pending read to fail")
	}
	select {
	case <-channel.receiveDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for receive failure")
	}
	writes := raw.writesN
	_, err = channel.SWDIO(context.Background(), []byte{1}, []byte{1}, 1)
	if !errors.Is(err, want) || !errors.Is(err, ErrChannelPoisoned) {
		t.Fatalf("SWDIO() error = %v, want original error and ErrChannelPoisoned", err)
	}
	if raw.writesN != writes {
		t.Fatalf("bulk writes after receive failure = %d, want 0", raw.writesN-writes)
	}
}

func TestPoisonedChannelStillClosesAndRetriesCleanup(t *testing.T) {
	releaseFailure := errors.New("release failed")
	raw := &fakeUSBDevice{writeErr: 1, releaseErr: releaseFailure}
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
	if _, err := channel.SWDIO(context.Background(), []byte{1}, []byte{1}, 1); !errors.Is(err, ErrChannelPoisoned) {
		t.Fatalf("SWDIO() error = %v, want ErrChannelPoisoned", err)
	}
	if err := channel.Close(); !errors.Is(err, releaseFailure) {
		t.Fatalf("first Close() error = %v, want %v", err, releaseFailure)
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
