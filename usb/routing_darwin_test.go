//go:build darwin && cgo

package usb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"
)

type routingDarwinDevice struct {
	fakeDarwinInterfaceDevice
	raw       []byte
	alternate uint8
	controls  int
}

func (d *routingDarwinDevice) control(request darwinControlRequest, data []byte) (uint16, error) {
	d.controls++
	var reply []byte
	switch {
	case request.requestType == 0x81 && request.request == 0x0a:
		reply = []byte{d.alternate}
	case request.requestType == 0x80 && request.request == 0x08:
		reply = []byte{7}
	case request.requestType == 0x80 && request.request == 0x06 && request.value == 0x0100:
		reply = make([]byte, 18)
		reply[0], reply[1], reply[17] = 18, 1, 1
	case request.requestType == 0x80 && request.request == 0x06 && request.value == 0x0200:
		reply = d.raw
	default:
		return 0, fmt.Errorf("unexpected control request: %#v", request)
	}
	return uint16(copy(data, reply)), nil
}

type routingDarwinInterface struct {
	fakeDarwinInterface
	native      *testIOKitNative
	opens       int
	pipeQueries int
}

func (i *routingDarwinInterface) pipes() ([]darwinPipe, error) {
	i.pipeQueries++
	return i.fakeDarwinInterface.pipes()
}

func (i *routingDarwinInterface) openBulkTransfers(routes map[uint8]darwinPipe) (bulkTransferEngine, error) {
	i.opens++
	e := newIOKitBulkEngine(&iokitInterface{}, routes)
	e.openNative = func() (darwinBulkNative, error) { return i.native, nil }
	ready := make(chan error, 1)
	go e.run(ready)
	return e, <-ready
}

func newDarwinRoutingClaim(t *testing.T, raw []byte, pipes []darwinPipe) (*ClaimedInterface, *routingDarwinDevice, *routingDarwinInterface) {
	t.Helper()
	i := &routingDarwinInterface{native: &testIOKitNative{}}
	i.pipesValue = pipes
	d := &routingDarwinDevice{raw: raw}
	d.iface = i
	device := &Device{handle: d}
	claim, err := device.ClaimInterface(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		i.pipesErr = nil
		if err := device.Close(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return claim, d, i
}

func assertCanceledDarwinRouteSubmission(t *testing.T, claim *ClaimedInterface, d *routingDarwinDevice, i *routingDarwinInterface, endpoint uint8) {
	t.Helper()
	controls, queries := d.controls, i.pipeQueries
	canceled, cancelSubmission := context.WithCancel(t.Context())
	cancelSubmission()
	if _, err := claim.SubmitBulk(canceled, endpoint, make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled submission = %v", err)
	}
	if d.controls != controls || i.pipeQueries != queries || i.opens != 0 {
		t.Fatal("canceled submission reached metadata or native routing")
	}
}

func TestDarwinFailedAlternateReloadsRoutesForBulk(t *testing.T) {
	for _, actual := range []uint8{0, 1} {
		t.Run(fmt.Sprint(actual), func(t *testing.T) {
			raw := rawConfiguration(7, 1,
				interfaceDescriptor(0, 0, 1), endpointDescriptor(0x81),
				interfaceDescriptor(0, 1, 1), endpointDescriptor(0x82))
			claim, d, i := newDarwinRoutingClaim(t, raw, []darwinPipe{{endpoint: 0x81, ref: 7}})
			if _, err := claim.Endpoint(t.Context(), 0x81); err != nil {
				t.Fatal(err)
			}
			want := errors.New("alternate selection failed")
			i.setAltErr = want
			if err := claim.SetAltSetting(1); !errors.Is(err, want) {
				t.Fatalf("SetAltSetting() = %v", err)
			}
			d.alternate = actual
			endpoint := uint8(0x81 + actual)
			i.pipesValue = []darwinPipe{{endpoint: endpoint, ref: 3}}
			if _, err := claim.Endpoint(t.Context(), endpoint); err != nil {
				t.Fatal(err)
			}
			assertCanceledDarwinRouteSubmission(t, claim, d, i, endpoint)
			i.pipesErr = errors.New("pipe reload failed")
			if _, err := claim.SubmitBulk(t.Context(), endpoint, make([]byte, 8)); !errors.Is(err, i.pipesErr) {
				t.Fatalf("SubmitBulk() with failed reload = %v", err)
			}
			if i.opens != 0 || len(i.native.requests) != 0 {
				t.Fatal("failed reload opened an engine or submitted native traffic")
			}
			i.pipesErr = nil
			transfer, err := claim.SubmitBulk(t.Context(), endpoint, make([]byte, 8))
			if err != nil {
				t.Fatal(err)
			}
			if len(i.alternates) != 1 || i.alternates[0] != 1 {
				t.Fatalf("replayed alternate selection: %v", i.alternates)
			}
			i.native.mu.Lock()
			r := i.native.requests[0]
			pipe, input := r.pipe, r.input
			r.ready = true
			r.event = darwinBulkEvent{available: true, count: 5}
			i.native.mu.Unlock()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if count, err := transfer.Wait(ctx); err != nil || count != 5 || pipe != 3 || !input {
				t.Fatalf("bulk completion=%d/%v native pipe=%d input=%t", count, err, pipe, input)
			}
		})
	}
}

func TestDarwinAlternateKeepsConfigurationPacketSize(t *testing.T) {
	for _, encoded := range []uint16{1024, 1024 | 2<<11} {
		t.Run(fmt.Sprint(encoded), func(t *testing.T) {
			endpoint := endpointDescriptorWithType(0x81, TransferInterrupt)
			endpoint[6] = 1
			binary.LittleEndian.PutUint16(endpoint[4:6], encoded)
			raw := rawConfiguration(7, 1,
				interfaceDescriptor(0, 0, 1), endpoint,
				interfaceDescriptor(0, 1, 1), endpoint)
			claim, d, i := newDarwinRoutingClaim(t, raw, []darwinPipe{
				{endpoint: 0x81, ref: 4, transferType: uint8(TransferInterrupt), maxPacketSize: 16384},
			})
			before, err := claim.Endpoint(t.Context(), 0x81)
			if err != nil {
				t.Fatal(err)
			}
			configuration, err := claim.device.ActiveConfiguration(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := configuration.Interfaces[0].Alternates[1].Endpoints[0]
			if before.MaxPacketSize != 1024 || want != before {
				t.Fatalf("independent descriptors=%#v/%#v", before, want)
			}
			d.alternate = 1
			if err := claim.SetAltSetting(1); err != nil {
				t.Fatal(err)
			}
			after, err := claim.Endpoint(t.Context(), 0x81)
			if err != nil || after != want {
				t.Fatalf("Endpoint() after selection=%#v/%v, want %#v (IOKit size %d)", after, err, want, i.pipesValue[0].maxPacketSize)
			}
		})
	}
}
