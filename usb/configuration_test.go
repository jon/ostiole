package usb

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type descriptorReply struct {
	request uint8
	value   uint16
	data    []byte
	short   bool
	err     error
}

type scriptedDescriptorDevice struct {
	t       *testing.T
	replies []descriptorReply
	calls   int
	after   func(int)
}

func (d *scriptedDescriptorDevice) ControlTransfer(_ context.Context, requestType, request uint8, value, index uint16, data []byte) (int, error) {
	d.t.Helper()
	if requestType != requestTypeStandardDeviceIn || index != 0 || d.calls >= len(d.replies) {
		d.t.Fatalf("unexpected control transfer type=%#02x request=%#02x value=%#04x index=%d", requestType, request, value, index)
	}
	reply := d.replies[d.calls]
	d.calls++
	if d.after != nil {
		d.after(d.calls)
	}
	if request != reply.request || value != reply.value {
		d.t.Fatalf("control transfer %d = request %#02x value %#04x, want %#02x/%#04x", d.calls, request, value, reply.request, reply.value)
	}
	copy(data, reply.data)
	if reply.short {
		return len(reply.data) - 1, reply.err
	}
	if reply.err != nil {
		return 0, reply.err
	}
	return len(reply.data), nil
}

func TestActiveConfigurationReturnsGroupedDescriptorSnapshot(t *testing.T) {
	raw := []byte{
		9, 2, 53, 0, 2, 7, 0, 0x80, 50,
		9, 4, 0, 0, 2, 0xff, 0xff, 0xff, 0,
		7, 5, 0x01, 2, 64, 0, 0,
		7, 5, 0x81, 2, 64, 0, 0,
		3, 0x30, 1,
		9, 4, 0, 1, 0, 0xfe, 1, 2, 0,
		9, 4, 1, 0, 0, 3, 1, 1, 0,
	}
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
		{request: 8, data: []byte{7}},
		{request: 6, value: 0x0100, data: deviceDescriptor},
		{request: 6, value: 0x0200, data: raw[:9]},
		{request: 6, value: 0x0200, data: raw},
		{request: 8, data: []byte{7}},
	}}

	got, err := activeConfiguration(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	want := Configuration{Value: 7, Interfaces: []Interface{
		{Number: 0, Alternates: []AlternateSetting{
			{Number: 0, Class: 0xff, Subclass: 0xff, Protocol: 0xff, Endpoints: []Endpoint{
				{Address: 0x01, TransferType: TransferBulk, MaxPacketSize: 64},
				{Address: 0x81, TransferType: TransferBulk, MaxPacketSize: 64},
			}},
			{Number: 1, Class: 0xfe, Subclass: 1, Protocol: 2},
		}},
		{Number: 1, Alternates: []AlternateSetting{{Number: 0, Class: 3, Subclass: 1, Protocol: 1}}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ActiveConfiguration() = %#v, want %#v", got, want)
	}
}

func TestActiveConfigurationFindsNonSequentialActiveValue(t *testing.T) {
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 2
	first := singleInterfaceConfiguration(1)
	second := singleInterfaceConfiguration(7)
	device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
		{request: 8, data: []byte{7}},
		{request: 6, value: 0x0100, data: deviceDescriptor},
		{request: 6, value: 0x0200, data: first[:9]},
		{request: 6, value: 0x0201, data: second[:9]},
		{request: 6, value: 0x0201, data: second},
		{request: 8, data: []byte{7}},
	}}
	got, err := activeConfiguration(context.Background(), device)
	if err != nil || got.Value != 7 {
		t.Fatalf("ActiveConfiguration() = %#v, %v", got, err)
	}
}

func TestActiveConfigurationScansAllConfigurationHeaders(t *testing.T) {
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 2
	first := singleInterfaceConfiguration(7)
	second := singleInterfaceConfiguration(9)
	device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
		{request: 8, data: []byte{7}},
		{request: 6, value: 0x0100, data: deviceDescriptor},
		{request: 6, value: 0x0200, data: first[:9]},
		{request: 6, value: 0x0201, data: second[:9]},
		{request: 6, value: 0x0200, data: first},
		{request: 8, data: []byte{7}},
	}}
	got, err := activeConfiguration(context.Background(), device)
	if err != nil || got.Value != 7 {
		t.Fatalf("ActiveConfiguration() = %#v, %v", got, err)
	}
}

func TestActiveConfigurationRejectsDuplicateActiveValues(t *testing.T) {
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 2
	first := singleInterfaceConfiguration(7)
	second := singleInterfaceConfiguration(7)
	device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
		{request: 8, data: []byte{7}},
		{request: 6, value: 0x0100, data: deviceDescriptor},
		{request: 6, value: 0x0200, data: first[:9]},
		{request: 6, value: 0x0201, data: second[:9]},
	}}
	if _, err := activeConfiguration(context.Background(), device); err == nil || !strings.Contains(err.Error(), "appears at indices 0 and 1") {
		t.Fatalf("ActiveConfiguration() error = %v", err)
	}
}

func TestActiveConfigurationRejectsChangedDescriptorValue(t *testing.T) {
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	header := singleInterfaceConfiguration(7)
	full := singleInterfaceConfiguration(8)
	device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
		{request: 8, data: []byte{7}},
		{request: 6, value: 0x0100, data: deviceDescriptor},
		{request: 6, value: 0x0200, data: header[:9]},
		{request: 6, value: 0x0200, data: full},
	}}
	if _, err := activeConfiguration(context.Background(), device); err == nil || !strings.Contains(err.Error(), "value changed") {
		t.Fatalf("ActiveConfiguration() error = %v", err)
	}
}

func TestActiveConfigurationRejectsChangedConfigurationLength(t *testing.T) {
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	full := []byte{10, 2, 19, 0, 1, 7, 0, 0x80, 50, 0, 9, 4, 0, 0, 0, 0xff, 0xff, 0xff, 0}
	header := append([]byte(nil), full[:9]...)
	header[0] = 9
	device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
		{request: 8, data: []byte{7}},
		{request: 6, value: 0x0100, data: deviceDescriptor},
		{request: 6, value: 0x0200, data: header},
		{request: 6, value: 0x0200, data: full},
		{request: 8, data: []byte{7}},
	}}
	if _, err := activeConfiguration(context.Background(), device); err == nil || !strings.Contains(err.Error(), "malformed configuration descriptor") {
		t.Fatalf("ActiveConfiguration() error = %v", err)
	}
}

func TestActiveConfigurationRejectsChangedActiveValue(t *testing.T) {
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	raw := singleInterfaceConfiguration(7)
	device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
		{request: 8, data: []byte{7}},
		{request: 6, value: 0x0100, data: deviceDescriptor},
		{request: 6, value: 0x0200, data: raw[:9]},
		{request: 6, value: 0x0200, data: raw},
		{request: 8, data: []byte{9}},
	}}
	_, err := activeConfiguration(context.Background(), device)
	if err == nil || !strings.Contains(err.Error(), "active configuration changed from 7 to 9") {
		t.Fatalf("ActiveConfiguration() error = %v", err)
	}
	if errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ActiveConfiguration() error = %v, unexpectedly matches ErrNotConfigured", err)
	}
}

func TestActiveConfigurationReportsTransitionToUnconfigured(t *testing.T) {
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	raw := singleInterfaceConfiguration(7)
	device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
		{request: 8, data: []byte{7}},
		{request: 6, value: 0x0100, data: deviceDescriptor},
		{request: 6, value: 0x0200, data: raw[:9]},
		{request: 6, value: 0x0200, data: raw},
		{request: 8, data: []byte{0}},
	}}
	if _, err := activeConfiguration(context.Background(), device); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ActiveConfiguration() error = %v, want ErrNotConfigured", err)
	}
}

func TestConfigurationRejectsAmbiguousEndpointAddresses(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "endpoint zero", raw: rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), endpointDescriptor(0x00)), want: "invalid endpoint address"},
		{name: "endpoint zero IN", raw: rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), endpointDescriptor(0x80)), want: "invalid endpoint address"},
		{name: "reserved address bits", raw: rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), endpointDescriptor(0x91)), want: "invalid endpoint address"},
		{name: "duplicate in alternate", raw: rawConfiguration(1, 1, interfaceDescriptor(0, 0, 2), endpointDescriptor(0x81), endpointDescriptor(0x81)), want: "duplicate endpoint"},
		{name: "directionless control duplicate", raw: rawConfiguration(1, 1, interfaceDescriptor(0, 0, 2), endpointDescriptorWithType(0x01, TransferControl), endpointDescriptorWithType(0x81, TransferControl)), want: "conflicting control endpoint number"},
		{name: "directionless control duplicate after bulk", raw: rawConfiguration(1, 1, interfaceDescriptor(0, 0, 2), endpointDescriptor(0x81), endpointDescriptorWithType(0x01, TransferControl)), want: "conflicting control endpoint number"},
		{name: "duplicate across interfaces", raw: rawConfiguration(1, 2, interfaceDescriptor(0, 0, 1), endpointDescriptor(0x81), interfaceDescriptor(1, 0, 1), endpointDescriptor(0x81)), want: "belongs to interfaces"},
		{name: "control conflict across interfaces", raw: rawConfiguration(1, 2, interfaceDescriptor(0, 0, 1), endpointDescriptorWithType(0x01, TransferControl), interfaceDescriptor(1, 0, 1), endpointDescriptor(0x81)), want: "control endpoint number"},
		{name: "control conflict after bulk across interfaces", raw: rawConfiguration(1, 2, interfaceDescriptor(0, 0, 1), endpointDescriptor(0x81), interfaceDescriptor(1, 0, 1), endpointDescriptorWithType(0x01, TransferControl)), want: "control endpoint number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseConfiguration(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseConfiguration() error = %v", err)
			}
		})
	}
}

func TestConfigurationAllowsEndpointReuseAcrossAlternateSettings(t *testing.T) {
	tests := [][]byte{
		rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), endpointDescriptor(0x81), interfaceDescriptor(0, 1, 1), endpointDescriptor(0x81)),
		rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), endpointDescriptorWithType(0x01, TransferControl), interfaceDescriptor(0, 1, 1), endpointDescriptor(0x81)),
	}
	for _, raw := range tests {
		configuration, err := parseConfiguration(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(configuration.Interfaces) != 1 || len(configuration.Interfaces[0].Alternates) != 2 {
			t.Fatalf("configuration = %#v", configuration)
		}
	}
}

func TestConfigurationValidatesAlternateSettings(t *testing.T) {
	if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 1, 0))); err == nil || !strings.Contains(err.Error(), "interface 0 has no alternate setting zero") {
		t.Fatalf("parseConfiguration() error = %v", err)
	}
	configuration, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 1, 0), interfaceDescriptor(0, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.Interfaces) != 1 || len(configuration.Interfaces[0].Alternates) != 2 {
		t.Fatalf("configuration = %#v", configuration)
	}
	if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 0), interfaceDescriptor(0, 2, 0))); err == nil || !strings.Contains(err.Error(), "alternate setting 2 is outside range 0 through 1") {
		t.Fatalf("parseConfiguration() error = %v", err)
	}
}

func TestConfigurationRejectsInvalidInterfaceHierarchy(t *testing.T) {
	long := append(interfaceDescriptor(0, 0, 0), 0)
	long[0] = 10
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "zero interfaces", raw: rawConfiguration(1, 0), want: "configuration descriptor has no interfaces"},
		{name: "long interface descriptor", raw: rawConfiguration(1, 1, long), want: "invalid interface descriptor length 10"},
		{name: "noncontiguous interface numbers", raw: rawConfiguration(1, 2, interfaceDescriptor(0, 0, 0), interfaceDescriptor(2, 0, 0)), want: "interface 2 is outside configuration interface count 2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseConfiguration(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseConfiguration() error = %v", err)
			}
		})
	}
}

func TestConfigurationValidatesConfigurationAttributes(t *testing.T) {
	for _, attributes := range []byte{0, 0x81} {
		raw := singleInterfaceConfiguration(1)
		raw[7] = attributes
		if _, err := parseConfiguration(raw); err == nil || !strings.Contains(err.Error(), "malformed configuration descriptor") {
			t.Fatalf("parseConfiguration() error = %v", err)
		}
	}
}

func TestConfigurationValidatesEndpointDescriptorLengths(t *testing.T) {
	long := append(endpointDescriptor(0x81), 0)
	long[0] = 8
	if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), long)); err == nil || !strings.Contains(err.Error(), "invalid endpoint descriptor length 8") {
		t.Fatalf("parseConfiguration() error = %v", err)
	}
	vendor := append(endpointDescriptor(0x81), 0, 0)
	vendor[0] = 9
	if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), vendor)); err == nil || !strings.Contains(err.Error(), "outside audio interface") {
		t.Fatalf("parseConfiguration() error = %v", err)
	}
	audioInterface := interfaceDescriptor(0, 0, 1)
	audioInterface[5] = 1
	audio := append(endpointDescriptorWithType(0x81, TransferIsochronous), 0, 0)
	audio[0], audio[6] = 9, 1
	if _, err := parseConfiguration(rawConfiguration(1, 1, audioInterface, audio)); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationValidatesEndpointMaximumPacketSize(t *testing.T) {
	for _, encoded := range []uint16{0, 1025, 0x0801, 0x1801, 0x8001} {
		descriptor := endpointDescriptor(0x81)
		binary.LittleEndian.PutUint16(descriptor[4:6], encoded)
		if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), descriptor)); err == nil || !strings.Contains(err.Error(), "invalid endpoint maximum packet size") {
			t.Fatalf("parseConfiguration() error = %v", err)
		}
	}
	descriptor := endpointDescriptor(0x81)
	binary.LittleEndian.PutUint16(descriptor[4:6], 1024)
	configuration, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), descriptor))
	if err != nil {
		t.Fatal(err)
	}
	if got := configuration.Interfaces[0].Alternates[0].Endpoints[0].MaxPacketSize; got != 1024 {
		t.Fatalf("MaxPacketSize = %d, want 1024", got)
	}
	zeroInterrupt := endpointDescriptorWithType(0x81, TransferInterrupt)
	zeroInterrupt[4], zeroInterrupt[6] = 0, 1
	configuration, err = parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), zeroInterrupt))
	if err != nil {
		t.Fatal(err)
	}
	if got := configuration.Interfaces[0].Alternates[0].Endpoints[0].MaxPacketSize; got != 0 {
		t.Fatalf("MaxPacketSize = %d, want 0", got)
	}
	for _, encoded := range []uint16{0x0800 | 512, 0x1000 | 682} {
		highBandwidth := endpointDescriptorWithType(0x81, TransferIsochronous)
		highBandwidth[6] = 1
		binary.LittleEndian.PutUint16(highBandwidth[4:6], encoded)
		if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), highBandwidth)); err == nil || !strings.Contains(err.Error(), "invalid endpoint maximum packet size") {
			t.Fatalf("parseConfiguration() error = %v", err)
		}
	}
	for _, encoded := range []uint16{0x0800 | 513, 0x1000 | 683} {
		highBandwidth := endpointDescriptorWithType(0x81, TransferIsochronous)
		highBandwidth[6] = 1
		binary.LittleEndian.PutUint16(highBandwidth[4:6], encoded)
		configuration, err = parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), highBandwidth))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := configuration.Interfaces[0].Alternates[0].Endpoints[0].MaxPacketSize, encoded&0x07ff; got != want {
			t.Fatalf("MaxPacketSize = %d, want %d", got, want)
		}
	}
}

func TestConfigurationValidatesEndpointAttributesAndIntervals(t *testing.T) {
	tests := []struct {
		name       string
		descriptor []byte
		want       string
	}{
		{name: "synchronized explicit feedback", descriptor: []byte{7, 5, 0x81, 0x15, 64, 0, 1}, want: "invalid endpoint attributes"},
		{name: "zero isochronous interval", descriptor: []byte{7, 5, 0x81, 0x01, 64, 0, 0}, want: "invalid endpoint interval"},
		{name: "long isochronous interval", descriptor: []byte{7, 5, 0x81, 0x01, 64, 0, 17}, want: "invalid endpoint interval"},
		{name: "zero interrupt interval", descriptor: []byte{7, 5, 0x81, 0x03, 64, 0, 0}, want: "invalid endpoint interval"},
		{name: "slow high-bandwidth isochronous interval", descriptor: []byte{7, 5, 0x81, 0x01, 1, 10, 2}, want: "invalid endpoint interval"},
		{name: "slow high-bandwidth interrupt interval", descriptor: []byte{7, 5, 0x81, 0x03, 1, 10, 2}, want: "invalid endpoint interval"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), test.descriptor)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseConfiguration() error = %v", err)
			}
		})
	}
}

func TestConfigurationIgnoresReservedEndpointAttributeBits(t *testing.T) {
	for _, descriptor := range [][]byte{
		{7, 5, 0x81, 0x82, 64, 0, 0},
		{7, 5, 0x81, 0x06, 64, 0, 0},
		{7, 5, 0x81, 0x13, 64, 0, 1},
		{7, 5, 0x81, 0xc1, 64, 0, 1},
		{7, 5, 0x81, 0x31, 64, 0, 1},
	} {
		if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), descriptor)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActiveConfigurationRejectsUnavailableMetadata(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{{request: 8, data: []byte{0}}}}
		if _, err := activeConfiguration(context.Background(), device); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("ActiveConfiguration() error = %v, want ErrNotConfigured", err)
		}
	})
	t.Run("no matching descriptor", func(t *testing.T) {
		deviceDescriptor := make([]byte, 18)
		deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
		configuration := singleInterfaceConfiguration(1)
		device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
			{request: 8, data: []byte{7}},
			{request: 6, value: 0x0100, data: deviceDescriptor},
			{request: 6, value: 0x0200, data: configuration[:9]},
		}}
		if _, err := activeConfiguration(context.Background(), device); err == nil || !strings.Contains(err.Error(), "has no descriptor") {
			t.Fatalf("ActiveConfiguration() error = %v", err)
		}
	})
	for _, test := range []struct {
		name   string
		length byte
	}{{name: "short device descriptor header", length: 17}, {name: "long device descriptor header", length: 19}} {
		t.Run(test.name, func(t *testing.T) {
			deviceDescriptor := make([]byte, 18)
			deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = test.length, 1, 1
			device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
				{request: 8, data: []byte{1}},
				{request: 6, value: 0x0100, data: deviceDescriptor},
			}}
			if _, err := activeConfiguration(context.Background(), device); err == nil || !strings.Contains(err.Error(), "malformed device descriptor") {
				t.Fatalf("ActiveConfiguration() error = %v", err)
			}
		})
	}
}

func TestActiveConfigurationRejectsShortAndMalformedDescriptors(t *testing.T) {
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	t.Run("short transfer", func(t *testing.T) {
		device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{{request: 8, data: []byte{1}, short: true}}}
		if _, err := activeConfiguration(context.Background(), device); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
		}
	})
	tests := [][]byte{
		{10, 2, 9, 0, 0, 1, 0, 0x80, 50},
		{10, 2, 19, 0, 1, 1, 0, 0x80, 50, 0, 9, 4, 0, 0, 0, 0xff, 0xff, 0xff, 0},
		{9, 2, 11, 0, 0, 1, 0, 0x80, 50, 0, 4},
		{9, 2, 16, 0, 0, 1, 0, 0x80, 50, 7, 5, 0x81, 2, 64, 0, 0},
		{9, 2, 18, 0, 1, 1, 0, 0x80, 50, 9, 4, 0, 0, 1, 0xff, 0xff, 0xff, 0},
		{9, 2, 27, 0, 1, 1, 0, 0x80, 50, 9, 4, 0, 0, 0, 0xff, 0xff, 0xff, 0, 9, 4, 0, 0, 0, 0xff, 0xff, 0xff, 0},
	}
	for index, raw := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			device := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{
				{request: 8, data: []byte{1}},
				{request: 6, value: 0x0100, data: deviceDescriptor},
				{request: 6, value: 0x0200, data: raw[:9]},
				{request: 6, value: 0x0200, data: raw},
			}}
			if _, err := activeConfiguration(context.Background(), device); err == nil {
				t.Fatal("ActiveConfiguration() succeeded")
			}
		})
	}
}

func TestActiveConfigurationStopsBeforeTrafficWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	device := &scriptedDescriptorDevice{t: t}
	if _, err := activeConfiguration(ctx, device); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if device.calls != 0 {
		t.Fatalf("control transfers = %d, want 0", device.calls)
	}
}

func TestActiveConfigurationStopsBetweenDescriptorTransfersWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	deviceDescriptor := make([]byte, 18)
	deviceDescriptor[0], deviceDescriptor[1], deviceDescriptor[17] = 18, 1, 1
	header := []byte{9, 2, 9, 0, 0, 1, 0, 0x80, 50}
	device := &scriptedDescriptorDevice{
		t: t,
		replies: []descriptorReply{
			{request: 8, data: []byte{1}},
			{request: 6, value: 0x0100, data: deviceDescriptor},
			{request: 6, value: 0x0200, data: header},
		},
		after: func(calls int) {
			if calls == 3 {
				cancel()
			}
		},
	}
	if _, err := activeConfiguration(ctx, device); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if device.calls != 3 {
		t.Fatalf("control transfers = %d, want 3", device.calls)
	}
}

func rawConfiguration(value, interfaces uint8, descriptors ...[]byte) []byte {
	raw := []byte{9, 2, 0, 0, interfaces, value, 0, 0x80, 50}
	for _, descriptor := range descriptors {
		raw = append(raw, descriptor...)
	}
	binary.LittleEndian.PutUint16(raw[2:4], uint16(len(raw)))
	return raw
}

func singleInterfaceConfiguration(value uint8) []byte {
	return rawConfiguration(value, 1, interfaceDescriptor(0, 0, 0))
}

func interfaceDescriptor(number, alternate, endpoints uint8) []byte {
	return []byte{9, 4, number, alternate, endpoints, 0xff, 0xff, 0xff, 0}
}

func endpointDescriptor(address uint8) []byte {
	return endpointDescriptorWithType(address, TransferBulk)
}

func endpointDescriptorWithType(address uint8, transferType TransferType) []byte {
	return []byte{7, 5, address, byte(transferType), 64, 0, 0}
}
