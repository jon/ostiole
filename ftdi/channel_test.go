package ftdi

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/usb"
)

type fakeUSBDevice struct {
	identity   usb.DeviceInfo
	claimed    uint8
	released   uint8
	request    uint8
	value      uint16
	index      uint16
	wroteEP    uint8
	readEP     uint8
	closed     bool
	writeN     []int
	readData   [][]byte
	controls   []controlRecord
	writes     [][]byte
	claimErr   error
	releaseErr error
	claim      *fakeUSBClaim
	writeErr   int
	writesN    int
	releases   int
}

func (d *fakeUSBDevice) Identity() usb.DeviceInfo {
	if d.identity == (usb.DeviceInfo{}) {
		return usb.DeviceInfo{VID: VID, PID: PIDFT232H}
	}
	return d.identity
}

type fakeUSBClaim struct {
	device *fakeUSBDevice
}

func (c *fakeUSBClaim) Close() error {
	c.device.released = c.device.claimed
	c.device.releases++
	if c.device.releaseErr != nil {
		return c.device.releaseErr
	}
	c.device.claim = nil
	return nil
}

type controlRecord struct {
	request uint8
	value   uint16
	index   uint16
}

func (d *fakeUSBDevice) claimInterface(iface uint8) (usbClaim, error) {
	if d.claimErr != nil {
		return nil, d.claimErr
	}
	d.claimed = iface
	claim := &fakeUSBClaim{device: d}
	d.claim = claim
	return claim, nil
}

func (d *fakeUSBDevice) ControlTransfer(_ context.Context, _ uint8, request uint8, value, index uint16, _ []byte) (int, error) {
	d.request, d.value, d.index = request, value, index
	d.controls = append(d.controls, controlRecord{
		request: request,
		value:   value,
		index:   index,
	})
	return 0, nil
}

func TestChannelOwnsAndClosesMPSSEMode(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	channel.settle = func(context.Context) error { return nil }

	if err := channel.enterMPSSE(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
	want := []controlRecord{
		{request: 0x00, value: 0, index: 1},
		{request: 0x00, value: 1, index: 1},
		{request: 0x00, value: 2, index: 1},
		{request: 0x09, value: 2, index: 1},
		{request: 0x0b, value: 0, index: 1},
		{request: 0x0b, value: 0x0200, index: 1},
		{request: 0x0b, value: 0, index: 1},
		{request: 0x09, value: 16, index: 1},
		{request: 0x00, value: 1, index: 1},
		{request: 0x00, value: 2, index: 1},
	}
	if len(raw.controls) != len(want) {
		t.Fatalf("control requests = %#v", raw.controls)
	}
	for index := range want {
		if raw.controls[index] != want[index] {
			t.Fatalf("control request %d = %#v", index, raw.controls[index])
		}
	}
	if raw.claimed != 0 || raw.released != 0 || !raw.closed {
		t.Fatalf("ownership = %#v", raw)
	}
}

func TestChannelRetainsUSBClaimAfterFailedRelease(t *testing.T) {
	want := errors.New("release failed")
	raw := &fakeUSBDevice{releaseErr: want}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() error = %v, want %v", err, want)
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

func (d *fakeUSBDevice) BulkWrite(_ context.Context, endpoint uint8, data []byte) (int, error) {
	d.writesN++
	if d.writeErr == d.writesN {
		return 0, errors.New("injected write failure")
	}
	d.wroteEP = endpoint
	d.writes = append(d.writes, append([]byte(nil), data...))
	if len(d.writeN) != 0 {
		count := d.writeN[0]
		d.writeN = d.writeN[1:]
		return count, nil
	}
	return len(data), nil
}

func TestChannelSynchronizesTheMPSSECommandStream(t *testing.T) {
	raw := &fakeUSBDevice{
		readData: [][]byte{{0x01, 0x60, 0xfa, 0xab}},
	}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}

	if err := channel.synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(raw.writes) != 1 ||
		string(raw.writes[0]) != string([]byte{0xab}) {
		t.Fatalf("synchronization writes = %x", raw.writes)
	}
}

func TestChannelConfiguresAConservativeMPSSEClock(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}

	if err := channel.configure(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x8a,
		0x97,
		0x8d,
		0x86, 74, 0,
		0x85,
		0x80, 0, 1,
		0x82, 0, 0,
	}
	if len(raw.writes) != 1 ||
		string(raw.writes[0]) != string(want) {
		t.Fatalf("configuration writes = %x, want %x", raw.writes, want)
	}
}

func (d *fakeUSBDevice) BulkRead(_ context.Context, endpoint uint8, data []byte) (int, error) {
	d.readEP = endpoint
	if len(d.readData) != 0 {
		payload := d.readData[0]
		d.readData = d.readData[1:]
		return copy(data, payload), nil
	}
	return len(data), nil
}

func TestChannelExchangesExactMPSSEPayloads(t *testing.T) {
	raw := &fakeUSBDevice{
		writeN: []int{2, 2},
		readData: [][]byte{
			{0x01, 0x60},
			{0x01, 0x60, 0xaa, 0xbb},
			{0x01, 0x60, 0xcc},
		},
	}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}

	if err := channel.writeExact(context.Background(), []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	got, err := channel.readPayload(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string([]byte{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("ReadPayload() = %x", got)
	}
}

func (d *fakeUSBDevice) Close() error {
	if d.claim != nil {
		if err := d.claim.Close(); err != nil {
			return err
		}
	}
	d.closed = true
	return nil
}

func TestChannelBindsOneExplicitMPSSEPort(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claimUSB(); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.control(context.Background(), 0x0b, 0x0200); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.bulkWrite(context.Background(), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.bulkRead(context.Background(), make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := channel.releaseUSB(); err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
	if raw.claimed != 0 || raw.released != 0 ||
		raw.request != 0x0b || raw.value != 0x0200 || raw.index != 1 ||
		raw.wroteEP != 0x02 || raw.readEP != 0x81 || !raw.closed {
		t.Fatalf("forwarded operations = %#v", raw)
	}
}

func TestChannelRejectsUnsupportedSelections(t *testing.T) {
	for _, port := range []Port{PortUnspecified, Port(3)} {
		if _, err := newChannel(&fakeUSBDevice{}, Config{Port: port, MaxClockHz: 400_000}); err == nil {
			t.Fatalf("newChannel(Port(%d)) succeeded", port)
		}
	}
}

func TestChannelDerivesProductFromUSBIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity usb.DeviceInfo
		port     Port
	}{
		{name: "another vendor", identity: usb.DeviceInfo{VID: 0x1234, PID: PIDFT232H}, port: PortA},
		{name: "unsupported product", identity: usb.DeviceInfo{VID: VID, PID: 0xffff}, port: PortA},
		{name: "FT232H port B", identity: usb.DeviceInfo{VID: VID, PID: PIDFT232H}, port: PortB},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newChannel(&fakeUSBDevice{identity: test.identity}, Config{Port: test.port, MaxClockHz: 400_000}); err == nil {
				t.Fatal("newChannel() succeeded")
			}
		})
	}
}
