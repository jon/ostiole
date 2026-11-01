package ftdi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jon/ostiole/usb"
)

type configurationPeer struct {
	legacy           bool
	pending          []byte
	synchronizations int
}

func (p *configurationPeer) respond(commands []byte) [][]byte {
	for i := 0; i < len(commands); i++ {
		switch command := commands[i]; command {
		case 0x80, 0x82, 0x86:
			i += 2
		case 0x85:
		case 0x8a, 0x8d, 0x97:
			if p.legacy {
				p.pending = append(p.pending, 0xfa, command)
			}
		case 0x87:
			if len(p.pending) == 0 {
				continue
			}
			packet := append([]byte{0x01, 0x60}, p.pending...)
			p.pending = nil
			return [][]byte{packet}
		default:
			p.pending = append(p.pending, 0xfa, command)
			if command == 0xab {
				p.synchronizations++
			}
		}
	}
	return nil
}

func prepareConfigurationFixture(t *testing.T, raw *fakeUSBDevice, port Port) (*Channel, error) {
	t.Helper()
	channel, err := newChannel(raw, Config{Port: port, MaxClockHz: 400_000})
	if err != nil {
		t.Fatal(err)
	}
	channel.settle = func(context.Context) error { return nil }
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = prepareChannel(ctx, channel)
	return channel, err
}

func TestOpenRejectsDelayedNonHConfigurationEchoes(t *testing.T) {
	peer := &configurationPeer{legacy: true}
	raw := &fakeUSBDevice{identity: usb.DeviceInfo{VID: VID, PID: PIDFT2232H}, writeResponses: peer.respond}
	channel, err := prepareConfigurationFixture(t, raw, PortA)
	if err == nil {
		_ = channel.Close()
		t.Fatal("legacy FT2232 accepted without checking delayed H-only command rejection")
	}
	if !raw.closed || raw.releases != 1 {
		t.Fatalf("configuration rejection lost cleanup: releases=%d closed=%t", raw.releases, raw.closed)
	}
}

func TestHConfigurationAndEveryPortBinding(t *testing.T) {
	for _, pid := range []uint16{PIDFT2232H, PIDFT4232H} {
		for _, port := range []Port{PortA, PortB} {
			t.Run(fmt.Sprintf("%x/%d", pid, port), func(t *testing.T) {
				peer := &configurationPeer{}
				raw := &fakeUSBDevice{identity: usb.DeviceInfo{VID: VID, PID: pid}, writeResponses: peer.respond}
				channel, err := prepareConfigurationFixture(t, raw, port)
				if err != nil {
					t.Fatal(err)
				}
				if err := channel.Close(); err != nil {
					t.Fatal(err)
				}
				iface := uint8(port - PortA)
				if raw.claimed != iface || raw.released != iface || raw.readEP != 0x81+2*iface || raw.wroteEP != 0x02+2*iface || peer.synchronizations != 2 {
					t.Fatalf("binding/sync: interface=%d/%d endpoints=%#x/%#x syncs=%d", raw.claimed, raw.released, raw.readEP, raw.wroteEP, peer.synchronizations)
				}
				for _, request := range raw.controls {
					if request.index != uint16(iface)+1 {
						t.Fatalf("control wIndex=%d, want%d", request.index, uint16(iface)+1)
					}
				}
			})
		}
	}
}

func TestOpenRejectsUnusableInputBeforeStartingReceiveEngine(t *testing.T) {
	for _, endpoint := range []usb.Endpoint{{TransferType: usb.TransferInterrupt, MaxPacketSize: 512}, {TransferType: usb.TransferBulk, MaxPacketSize: 0}, {TransferType: usb.TransferBulk, MaxPacketSize: 1}, {TransferType: usb.TransferBulk, MaxPacketSize: 2}} {
		raw := &fakeUSBDevice{endpointOverrides: map[uint8]usb.Endpoint{0x81: endpoint}}
		_, err := prepareConfigurationFixture(t, raw, PortA)
		if err == nil || raw.submittedReads != 0 || raw.releases != 1 || !raw.closed {
			t.Fatalf("invalid endpoint %+v: error=%v reads=%d release=%d closed=%t", endpoint, err, raw.submittedReads, raw.releases, raw.closed)
		}
	}
}

func TestConfigurationRejectionRetainsCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("interface release failed")
	peer := &configurationPeer{legacy: true}
	raw := &fakeUSBDevice{identity: usb.DeviceInfo{VID: VID, PID: PIDFT2232H}, writeResponses: peer.respond, releaseErr: cleanupErr}
	channel, err := prepareConfigurationFixture(t, raw, PortA)
	if err == nil || !strings.Contains(err.Error(), "verify H-part configuration") || !errors.Is(err, cleanupErr) || !channel.CleanupPending() || raw.closed {
		t.Fatalf("configuration/cleanup outcome: error=%v pending=%t closed=%t", err, channel.CleanupPending(), raw.closed)
	}
	raw.releaseErr = nil
	if err := channel.Close(); err != nil || channel.CleanupPending() || !raw.closed {
		t.Fatalf("cleanup retry: error=%v pending=%t closed=%t", err, channel.CleanupPending(), raw.closed)
	}
}

func TestOpenPreflightRejectsUsableMismatchedAttachment(t *testing.T) {
	cases := []struct {
		identity usb.DeviceInfo
		port     Port
	}{
		{usb.DeviceInfo{VID: 0x1234, PID: PIDFT2232H}, PortA},
		{usb.DeviceInfo{VID: VID, PID: 0xffff}, PortA},
		{usb.DeviceInfo{VID: VID, PID: PIDFT232H}, PortB},
		{usb.DeviceInfo{VID: VID, PID: PIDFT2232H}, PortUnspecified},
	}
	for _, test := range cases {
		peer := &configurationPeer{}
		raw := &fakeUSBDevice{identity: test.identity, writeResponses: peer.respond}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		channel, err := openChannel(ctx, raw, Config{Port: test.port, MaxClockHz: 400_000})
		cancel()
		if channel != nil || err == nil || raw.claim != nil || len(raw.controls) != 0 || len(raw.writes) != 0 || raw.submittedReads != 0 || raw.closed {
			t.Fatalf("mismatch crossed preflight: channel=%v error=%v claim=%v controls=%d writes=%d reads=%d closed=%t", channel, err, raw.claim, len(raw.controls), len(raw.writes), raw.submittedReads, raw.closed)
		}
	}
}
