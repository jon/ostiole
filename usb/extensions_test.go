package usb

import (
	"encoding/binary"
	"testing"
)

func extendedConfiguration(configExtra, interfaceExtra, endpointExtra int) []byte {
	header := []byte{9, 2, 0, 0, 1, 7, 0, 0x80, 50}
	iface := interfaceDescriptor(0, 0, 1)
	endpoint := endpointDescriptor(0x81)
	for range configExtra {
		header = append(header, 0xde)
	}
	for range interfaceExtra {
		iface = append(iface, 0xad)
	}
	for range endpointExtra {
		endpoint = append(endpoint, 0xbe)
	}
	header[0], iface[0], endpoint[0] = byte(len(header)), byte(len(iface)), byte(len(endpoint))
	raw := append(header, iface...)
	raw = append(raw, endpoint...)
	binary.LittleEndian.PutUint16(raw[2:4], uint16(len(raw)))
	return raw
}

func TestDescriptorExtensionsUseDefinedPrefixes(t *testing.T) {
	for _, row := range []struct {
		name                           string
		configuration, iface, endpoint int
	}{{"configuration", 3, 0, 0}, {"interface", 0, 2, 0}, {"endpoint8", 0, 0, 1}, {"endpoint9", 0, 0, 2}, {"all", 3, 2, 4}} {
		t.Run(row.name, func(t *testing.T) {
			raw := extendedConfiguration(row.configuration, row.iface, row.endpoint)
			got, err := parseConfiguration(raw)
			if err != nil || got.Value != 7 || len(got.Interfaces) != 1 {
				t.Fatalf("extended descriptors: %+v %v", got, err)
			}
			setting := got.Interfaces[0].Alternates[0]
			if setting.Class != 0xff || len(setting.Endpoints) != 1 || setting.Endpoints[0] != (Endpoint{Address: 0x81, TransferType: TransferBulk, MaxPacketSize: 64}) {
				t.Fatalf("defined prefix changed: %+v", setting)
			}
		})
	}
}

func TestActiveConfigurationAcceptsExtendedHeaderPrefixes(t *testing.T) {
	raw := extendedConfiguration(3, 2, 4)
	device := make([]byte, 18)
	device[0], device[1], device[17] = 20, 1, 1
	f := &scriptedDescriptorDevice{t: t, replies: []descriptorReply{{request: 8, data: []byte{7}}, {request: 6, value: 0x100, data: device}, {request: 6, value: 0x200, data: raw[:9]}, {request: 6, value: 0x200, data: raw}, {request: 8, data: []byte{7}}}}
	if got, err := activeConfiguration(t.Context(), f); err != nil || got.Value != 7 {
		t.Fatalf("extended header prefixes: %+v %v", got, err)
	}
}

func TestZeroBandwidthIsochronousAlternate(t *testing.T) {
	zero := endpointDescriptorWithType(0x81, TransferIsochronous)
	zero[4], zero[5], zero[6] = 0, 0, 1
	active := append([]byte(nil), zero...)
	binary.LittleEndian.PutUint16(active[4:6], 256)
	raw := rawConfiguration(7, 1, interfaceDescriptor(0, 0, 1), zero, interfaceDescriptor(0, 1, 1), active)
	got, err := parseConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	settings := got.Interfaces[0].Alternates
	if len(settings) != 2 || settings[0].Endpoints[0].MaxPacketSize != 0 || settings[1].Endpoints[0].MaxPacketSize != 256 {
		t.Fatalf("zero bandwidth alternates: %+v", settings)
	}
}

func TestDescriptorPrefixesStillRequireDefinedBytes(t *testing.T) {
	iface := interfaceDescriptor(0, 0, 0)[:8]
	iface[0] = 8
	endpoint := endpointDescriptor(0x81)[:6]
	endpoint[0] = 6
	for _, raw := range [][]byte{rawConfiguration(1, 1, iface), rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), endpoint)} {
		if _, err := parseConfiguration(raw); err == nil {
			t.Fatal("accepted short defined prefix")
		}
	}
	raw := extendedConfiguration(0, 0, 0)
	raw[0] = 8
	if _, err := parseConfiguration(raw); err == nil {
		t.Fatal("accepted short configuration prefix")
	}
}

func TestZeroBandwidthRetainsEndpointBounds(t *testing.T) {
	for _, row := range []struct {
		kind     TransferType
		size     uint16
		interval byte
	}{
		{TransferBulk, 0, 0}, {TransferControl, 0, 0},
		{TransferIsochronous, 0x800, 1}, {TransferIsochronous, 0, 0},
	} {
		endpoint := endpointDescriptorWithType(0x81, row.kind)
		binary.LittleEndian.PutUint16(endpoint[4:6], row.size)
		endpoint[6] = row.interval
		if _, err := parseConfiguration(rawConfiguration(1, 1, interfaceDescriptor(0, 0, 1), endpoint)); err == nil {
			t.Fatalf("accepted invalid endpoint kind%d size%#x interval%d", row.kind, row.size, row.interval)
		}
	}
}
