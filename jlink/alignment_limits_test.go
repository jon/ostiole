package jlink

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestConfiguredSWDAlignsSamplesAcrossProductsAndFirmware(t *testing.T) {
	for _, pid := range []uint16{0x1020, 0x0101} {
		for _, record := range []string{delayedInputFirmwareRecord, "different firmware"} {
			t.Run(fmt.Sprintf("%x/%s", pid, firmwareString([]byte(record))), func(t *testing.T) {
				device := metadataPeer(t, nil)
				device.identity.PID = pid
				device.handleWrite = alignmentResponses(t, device)
				session := configuredSession(device, record)
				session.info.SelectedInterfaceKnown = true
				session.info.AvailableInterfaces = 2
				if err := session.ConfigureSWD(t.Context(), 100_000); err != nil {
					t.Fatal(err)
				}
				first, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 4)
				if err != nil {
					t.Fatal(err)
				}
				second, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 3)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(first, []byte{0x0a}) || !reflect.DeepEqual(second, []byte{0x05}) {
					t.Fatalf("sample chronology=%x,%x want0a,05", first, second)
				}
			})
		}
	}
}

func TestConfiguredSWDRequiresAdvertisedInterfaceWithUsableClaim(t *testing.T) {
	for _, known := range []bool{false, true} {
		device := metadataPeer(t, nil)
		session := configuredSession(device, "test firmware")
		session.info.SelectedInterfaceKnown = known
		session.info.AvailableInterfaces = 1
		err := session.ConfigureSWD(context.Background(), 100_000)
		if err == nil || !strings.Contains(err.Error(), "does not advertise configurable SWD") || device.writes != 0 {
			t.Fatalf("availability known=%t: error=%v writes=%d", known, err, device.writes)
		}
	}
}

func TestConfiguredClockEncodingsAndDescriptionGuards(t *testing.T) {
	cases := []struct {
		name          string
		capability    bool
		ceiling, base uint32
		divisor       uint16
		want          uint16
		failure       string
	}{
		{name: "wire ceiling", ceiling: 70_000_000, want: 65534},
		{name: "described wire ceiling", capability: true, ceiling: 70_000_000, base: 100_000_000, divisor: 1, want: 65534},
		{name: "device ceiling", capability: true, ceiling: 2_000_000, base: 2_000_000, divisor: 3, want: 666},
		{name: "sub kHz", capability: true, ceiling: 1_000, base: 500, divisor: 1, failure: "cannot provide a 1 kHz"},
		{name: "zero base", capability: true, ceiling: 100_000, base: 0, divisor: 1, failure: "invalid target clock description"},
		{name: "zero divisor", capability: true, ceiling: 100_000, base: 1_000_000, divisor: 0, failure: "invalid target clock description"},
	}
	for _, target := range []uint8{0, 1} {
		for _, test := range cases {
			t.Run(fmt.Sprintf("%d/%s", target, test.name), func(t *testing.T) {
				device := metadataPeer(t, nil)
				var speed []byte
				device.handleWrite = func(request []byte) (int, error) {
					switch request[0] {
					case 0xc7:
						device.responses = append(device.responses, []byte{0, 0, 0, 0})
					case 0xc0:
						response := make([]byte, 6)
						binary.LittleEndian.PutUint32(response, test.base)
						binary.LittleEndian.PutUint16(response[4:], test.divisor)
						device.responses = append(device.responses, response)
					case 0x05:
						speed = append([]byte(nil), request...)
					default:
						t.Fatalf("unexpected clock command%x", request)
					}
					return len(request), nil
				}
				session := configuredSession(device, "clock fixture")
				session.info.SelectedInterfaceKnown = true
				session.info.AvailableInterfaces = 3
				if test.capability {
					session.info.Capabilities = Capabilities{bytes: []byte{0, 2}}
				}
				configure := session.ConfigureSWD
				if target == 0 {
					configure = session.ConfigureJTAG
				}
				err := configure(t.Context(), test.ceiling)
				if test.failure != "" {
					if err == nil || !strings.Contains(err.Error(), test.failure) || speed != nil || session.configured {
						t.Fatalf("invalid clock accepted: error=%v speed=%x configured=%t", err, speed, session.configured)
					}
				} else if err != nil || !reflect.DeepEqual(speed, []byte{5, byte(test.want), byte(test.want >> 8)}) || session.ClockHz() != uint32(test.want)*1000 {
					t.Fatalf("clock result: error=%v speed=%x reported=%d", err, speed, session.ClockHz())
				}
			})
		}
	}
}

func TestSWDRejectsNegotiatedBitCountBeforeTraffic(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{request: []byte{0xc7, 1}, response: [][]byte{{0, 0, 0, 0}}}, {request: []byte{5, 100, 0}}})
	session := configuredSession(device, "small workspace")
	session.info.SelectedInterfaceKnown = true
	session.info.AvailableInterfaces = 2
	session.info.WorkspaceKnown, session.info.Workspace = true, 38
	if err := session.ConfigureSWD(t.Context(), 100_000); err != nil {
		t.Fatal(err)
	}
	before := device.writes
	_, err := session.SWDIO(t.Context(), make([]byte, 18), make([]byte, 18), 137)
	if session.MaxTransferBits() != 136 || err == nil || !strings.Contains(err.Error(), "transfer limit") || device.writes != before {
		t.Fatalf("bit limit: error=%v limit=%d writes=%d/%d", err, session.MaxTransferBits(), device.writes, before)
	}
}

func TestWriteExactRejectsInvalidCompletionCounts(t *testing.T) {
	for _, count := range []int{0, -1, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			device := metadataPeer(t, nil)
			device.handleWrite = func([]byte) (int, error) {
				if device.writes > 1 {
					return 0, errors.New("unexpected repeated OUT")
				}
				return count, nil
			}
			session := configuredSession(device, "count fixture")
			err := session.writeExact(t.Context(), []byte{5, 100, 0})
			if !errors.Is(err, ErrSessionPoisoned) || device.writes != 1 || len(device.submissions) != 1 {
				t.Fatalf("invalid write count%d: error=%v writes=%d submissions=%v", count, err, device.writes, device.submissions)
			}
			if count == 0 && !errors.Is(err, io.ErrNoProgress) {
				t.Fatalf("zero progress lost cause: %v", err)
			}
			if count != 0 && !strings.Contains(err.Error(), "invalid bulk-write count") {
				t.Fatalf("invalid count lost cause: %v", err)
			}
		})
	}
}

func alignmentResponses(t *testing.T, device *peerUSBDevice) func([]byte) (int, error) {
	scans := 0
	return func(request []byte) (int, error) {
		switch request[0] {
		case 0xc7:
			device.responses = append(device.responses, []byte{0, 0, 0, 0})
		case 0x05:
		case 0xcf:
			bits := binary.LittleEndian.Uint16(request[2:4])
			wantBits, samples := uint16(4), byte(0x0d)
			if scans == 1 {
				wantBits, samples = 3, 0x02
			}
			if bits != wantBits {
				t.Fatalf("scan bits=%d, want%d", bits, wantBits)
			}
			device.responses = append(device.responses, []byte{samples}, []byte{0})
			scans++
		default:
			t.Fatalf("unexpected command %x", request)
		}
		return len(request), nil
	}
}

func TestInvalidSpeedWriteLeavesSessionUnconfigured(t *testing.T) {
	for _, count := range []int{0, -1, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			device := metadataPeer(t, nil)
			speedWrites := 0
			device.handleWrite = func(request []byte) (int, error) {
				if request[0] == 0xc7 {
					device.responses = append(device.responses, []byte{0, 0, 0, 0})
					return len(request), nil
				}
				if request[0] != 5 {
					t.Fatalf("unexpected configuration command%x", request)
				}
				speedWrites++
				if speedWrites > 1 {
					return 0, errors.New("unexpected repeated speed write")
				}
				return count, nil
			}
			session := configuredSession(device, "invalid speed fixture")
			session.info.SelectedInterfaceKnown = true
			session.info.AvailableInterfaces = 2
			err := session.ConfigureSWD(t.Context(), 100_000)
			if !errors.Is(err, ErrSessionPoisoned) || session.configured || session.ClockHz() != 0 || speedWrites != 1 {
				t.Fatalf("invalid speed write%d: error=%v configured=%t clock=%d writes=%d", count, err, session.configured, session.ClockHz(), speedWrites)
			}
		})
	}
}
