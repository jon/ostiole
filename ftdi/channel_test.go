package ftdi

import (
	"context"
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
}

func (d *fakeUSBDevice) ClaimInterface(iface uint8) error {
	d.claimed = iface
	return nil
}

func (d *fakeUSBDevice) ReleaseInterface(iface uint8) error {
	d.released = iface
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
	return 0, nil
}

func (d *fakeUSBDevice) BulkWrite(
	_ context.Context,
	endpoint uint8,
	data []byte,
) (int, error) {
	d.wroteEP = endpoint
	return len(data), nil
}

func (d *fakeUSBDevice) BulkRead(
	_ context.Context,
	endpoint uint8,
	data []byte,
) (int, error) {
	d.readEP = endpoint
	return len(data), nil
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.Claim(); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.Control(
		context.Background(),
		0x0b,
		0x0200,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.BulkWrite(
		context.Background(),
		[]byte{1},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.BulkRead(
		context.Background(),
		make([]byte, 1),
	); err != nil {
		t.Fatal(err)
	}
	if err := channel.Release(); err != nil {
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

func TestNewChannelRejectsANilUSBDevice(t *testing.T) {
	if channel, err := NewChannel(nil, Config{}); channel != nil || err == nil {
		t.Fatalf("NewChannel(nil) = (%T, %v)", channel, err)
	}
}
