package ftdi

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/usb"
)

func TestOpenClosesAnInvalidSelection(t *testing.T) {
	raw := &fakeUSBDevice{}
	raw.identity = usb.DeviceInfo{VID: VID, PID: 0xffff}
	channel, err := openChannel(context.Background(), raw, Config{Port: PortA, MaxClockHz: 400_000})
	if channel != nil || err == nil || !raw.closed {
		t.Fatalf("openChannel() = (%T, %v), raw = %#v", channel, err, raw)
	}
}

func TestOpenRejectsInvalidClockBeforeAdapterTraffic(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := openChannel(t.Context(), raw, Config{Port: PortA})
	if channel != nil || err == nil || !raw.closed {
		t.Fatalf("openChannel() = (%T, %v), raw = %#v", channel, err, raw)
	}
	if len(raw.controls) != 0 || raw.releases != 0 {
		t.Fatalf("adapter traffic after invalid clock = controls %#v, releases %d", raw.controls, raw.releases)
	}
}

func TestOpenRejectsANilUSBDevice(t *testing.T) {
	if channel, err := Open(context.Background(), nil, Config{}); channel != nil || err == nil {
		t.Fatalf("Open(nil) = (%T, %v)", channel, err)
	}
}

func TestOpenCleansUpEveryFailedPreparationStage(t *testing.T) {
	tests := []struct {
		name string
		raw  *fakeUSBDevice
	}{
		{
			name: "mode entry",
			raw: &fakeUSBDevice{
				claimErr: errors.New("injected claim failure"),
			},
		},
		{
			name: "synchronization",
			raw: &fakeUSBDevice{
				readData: [][]byte{{0x01, 0x60, 0x00, 0x00}},
			},
		},
		{
			name: "configuration",
			raw: &fakeUSBDevice{
				readData: [][]byte{{0x01, 0x60, 0xfa, 0xab}},
				writeErr: 2,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel, err := newChannel(test.raw, Config{Port: PortA, MaxClockHz: 400_000})
			if err != nil {
				t.Fatal(err)
			}
			channel.settle = func(context.Context) error { return nil }

			ready, err := prepareChannel(context.Background(), channel)
			if ready != nil || err == nil || !test.raw.closed {
				t.Fatalf("prepareChannel() = (%T, %v), raw = %#v", ready, err, test.raw)
			}
			if test.name != "mode entry" && test.raw.releases != 1 {
				t.Fatalf("release count = %d, want 1", test.raw.releases)
			}
		})
	}
}

func TestOpenLeavesDeviceAvailableAfterFailedCleanup(t *testing.T) {
	wantRelease := errors.New("release failed")
	raw := &fakeUSBDevice{
		readData:   [][]byte{{0x01, 0x60, 0x00, 0x00}},
		releaseErr: wantRelease,
	}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	channel.settle = func(context.Context) error { return nil }

	ready, err := prepareChannel(context.Background(), channel)
	if ready != nil || !errors.Is(err, wantRelease) {
		t.Fatalf("prepareChannel() = (%T, %v), want nil and release error", ready, err)
	}
	if raw.closed || raw.releases != 1 {
		t.Fatalf("ownership after failed cleanup = %#v", raw)
	}
	raw.releaseErr = nil
	if err := raw.Close(); err != nil {
		t.Fatalf("device Close() retry: %v", err)
	}
	if !raw.closed || raw.releases != 2 {
		t.Fatalf("ownership after retry = %#v", raw)
	}
}
