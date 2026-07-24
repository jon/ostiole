package ftdi

import (
	"context"
	"errors"
	"testing"
)

type fakeUSBDevice struct {
	claimed  uint8
	released uint8
	request  uint8
	value    uint16
	index    uint16
	wroteEP  uint8
	readEP   uint8
	closed   bool
	writeN   []int
	readData [][]byte
	controls []controlRecord
	writes   [][]byte
	claimErr error
	writeErr int
	writesN  int
	releases int
}

type controlRecord struct {
	request uint8
	value   uint16
	index   uint16
}

func (d *fakeUSBDevice) ClaimInterface(iface uint8) error {
	if d.claimErr != nil {
		return d.claimErr
	}
	d.claimed = iface
	return nil
}

func (d *fakeUSBDevice) ReleaseInterface(iface uint8) error {
	d.released = iface
	d.releases++
	return nil
}

func (d *fakeUSBDevice) ControlTransfer(
	_ context.Context,
	_ uint8,
	request uint8,
	value, index uint16,
	_ []byte,
) (int, error) {
	d.request, d.value, d.index = request, value, index
	d.controls = append(d.controls, controlRecord{
		request: request,
		value:   value,
		index:   index,
	})
	return 0, nil
}

func TestChannelOwnsAndRestoresMPSSEMode(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{
		ProductID: PIDFT232H,
		Port:      PortA,
		Interface: SWD,
	})
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

func (d *fakeUSBDevice) BulkWrite(
	_ context.Context,
	endpoint uint8,
	data []byte,
) (int, error) {
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
	channel, err := newChannel(raw, Config{
		ProductID: PIDFT232H,
		Port:      PortA,
		Interface: SWD,
	})
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
	channel, err := newChannel(raw, Config{
		ClockHz:   400_000,
		ProductID: PIDFT232H,
		Port:      PortA,
		Interface: SWD,
	})
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

func (d *fakeUSBDevice) BulkRead(
	_ context.Context,
	endpoint uint8,
	data []byte,
) (int, error) {
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
	channel, err := newChannel(raw, Config{
		ProductID: PIDFT232H,
		Port:      PortA,
		Interface: SWD,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := channel.writeExact(
		context.Background(),
		[]byte{1, 2, 3, 4},
	); err != nil {
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
	d.closed = true
	return nil
}

func TestChannelBindsOneExplicitMPSSEPort(t *testing.T) {
	raw := &fakeUSBDevice{}
	channel, err := newChannel(raw, Config{
		ProductID: PIDFT232H,
		Port:      PortA,
		Interface: SWD,
		ClockHz:   400_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.claim(); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.control(
		context.Background(),
		0x0b,
		0x0200,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.bulkWrite(
		context.Background(),
		[]byte{1},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.bulkRead(
		context.Background(),
		make([]byte, 1),
	); err != nil {
		t.Fatal(err)
	}
	if err := channel.release(); err != nil {
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
	tests := []Config{
		{ProductID: 0xffff, Port: PortA, Interface: SWD},
		{ProductID: PIDFT232H, Port: PortB, Interface: SWD},
		{ProductID: PIDFT232H, Port: PortA},
	}
	for _, config := range tests {
		if _, err := newChannel(&fakeUSBDevice{}, config); err == nil {
			t.Fatalf("newChannel(%#v) succeeded", config)
		}
	}
}
