package ftdi

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jon/ostiole/usb"
)

func TestOpenProbeRejectsUnsupportedBinding(t *testing.T) {
	for _, port := range []Port{0, PortB} {
		p, err := OpenProbe(t.Context(), usb.DeviceInfo{VID: VID, PID: PIDFT232H}, port)
		if p != nil || err == nil {
			t.Fatal("invalid port opened")
		}
	}
	if p, err := OpenProbe(t.Context(), usb.DeviceInfo{}, PortA); p != nil || err == nil {
		t.Fatal("invalid device opened")
	}
}

func TestProbeRetainsFTDICleanupAfterFailedSetupAndDrain(t *testing.T) {
	setup, drain := errors.New("setup failed"), errors.New("drain failed")
	raw := &probeSetupFailure{
		fakeUSBDevice: &fakeUSBDevice{abortErr: drain, abortErrEP: 0x02},
		setupErr:      setup,
	}
	channel, err := openProbeChannel(t.Context(), raw, Config{Port: PortA, MaxClockHz: 100_000})
	if channel == nil || !errors.Is(err, setup) || !errors.Is(err, drain) {
		t.Fatalf("lost cleanup channel: %T, %v", channel, err)
	}
	if raw.closed || len(raw.controls) != 6 {
		t.Fatal("closed or reset before successful drain")
	}
	raw.abortErr = nil
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
	want := []controlRecord{
		{request: requestSetBitmode, value: 0, index: 1},
		{request: requestSetLatency, value: 16, index: 1},
		{request: requestReset, value: 1, index: 1},
		{request: requestReset, value: 2, index: 1},
	}
	if !raw.closed || len(raw.controlsAtClose) != 10 || !slices.Equal(raw.controlsAtClose[6:], want) {
		t.Fatalf("adapter cleanup before USB close: %v", raw.controlsAtClose)
	}
}

type probeSetupFailure struct {
	*fakeUSBDevice
	setupErr        error
	controlsAtClose []controlRecord
}

func (d *probeSetupFailure) ControlTransfer(ctx context.Context, requestType, request uint8, value, index uint16, data []byte) (int, error) {
	n, err := d.fakeUSBDevice.ControlTransfer(ctx, requestType, request, value, index, data)
	if request == requestSetBitmode && value == 0x0200 {
		return n, d.setupErr
	}
	return n, err
}

func (d *probeSetupFailure) Close() error {
	d.controlsAtClose = slices.Clone(d.controls)
	return d.fakeUSBDevice.Close()
}
