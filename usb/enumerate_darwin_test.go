//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeDarwinInventory struct {
	attachments []darwinAttachment
	err         error
	calls       int
}

func (f *fakeDarwinInventory) snapshot() ([]darwinAttachment, error) {
	f.calls++
	return f.attachments, f.err
}

func TestDarwinListFiltersAndSortsAttachments(t *testing.T) {
	inventory := &fakeDarwinInventory{attachments: []darwinAttachment{
		{vid: 0x0403, pid: 0x6014, location: 0x02123456, address: 9},
		{vid: 0x1366, pid: 0x0105, location: 0x01123456, address: 7},
		{vid: 0x0403, pid: 0x6001, location: 0x01123456, address: 3},
	}}
	bus := newDarwinEnumerator(inventory)

	got, err := bus.List(context.Background(), []DeviceFilter{{VID: 0x0403}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []DeviceInfo{
		{VID: 0x0403, PID: 0x6001, Bus: 1, Address: 3},
		{VID: 0x0403, PID: 0x6014, Bus: 2, Address: 9},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %#v, want %#v", got, want)
	}
}

func TestDarwinListValidatesContextAndReceiver(t *testing.T) {
	inventory := &fakeDarwinInventory{}
	bus := newDarwinEnumerator(inventory)

	var nilContext context.Context
	if _, err := bus.List(nilContext, nil); err == nil {
		t.Fatal("List(nil) succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bus.List(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("List(canceled) error = %v", err)
	}
	var nilBus *Enumerator
	if _, err := nilBus.List(context.Background(), nil); err == nil {
		t.Fatal("nil Enumerator.List succeeded")
	}
	if inventory.calls != 0 {
		t.Fatalf("snapshot calls = %d, want 0", inventory.calls)
	}
	var zeroBus Enumerator
	if _, err := zeroBus.List(context.Background(), nil); err == nil {
		t.Fatal("zero Enumerator.List succeeded")
	}
}

func TestDarwinListReportsInventoryFailure(t *testing.T) {
	want := errors.New("inventory unavailable")
	bus := newDarwinEnumerator(&fakeDarwinInventory{err: want})
	_, err := bus.List(context.Background(), []DeviceFilter{{VID: 1}})
	if !errors.Is(err, want) {
		t.Fatalf("List error = %v, want %v", err, want)
	}
}
