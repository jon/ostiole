//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"math"
	"testing"
)

type fakeDarwinBulkInterface struct {
	fakeDarwinInterface
	readRef, writeRef         uint8
	readTimeout, writeTimeout uint32
	readCount                 uint32
	readErr, writeErr         error
	reads, writes             int
}

func (f *fakeDarwinBulkInterface) readPipe(ref uint8, data []byte, timeout uint32) (uint32, error) {
	f.reads++
	f.readRef, f.readTimeout = ref, timeout
	return f.readCount, f.readErr
}

func (f *fakeDarwinBulkInterface) writePipe(ref uint8, data []byte, timeout uint32) error {
	f.writes++
	f.writeRef, f.writeTimeout = ref, timeout
	return f.writeErr
}

func TestDarwinBulkTransfersUseEndpointPipeRoutes(t *testing.T) {
	native := &fakeDarwinBulkInterface{readCount: 3}
	device := &Device{
		iface: native,
		routes: map[uint8]darwinPipe{
			0x02: {
				endpoint:     0x02,
				ref:          4,
				transferType: darwinBulkPipe,
			},
			0x81: {
				endpoint:     0x81,
				ref:          7,
				transferType: darwinBulkPipe,
			},
		},
	}
	if count, err := device.BulkWrite(context.Background(), 0x02, []byte{1, 2}); err != nil || count != 2 {
		t.Fatalf("BulkWrite = %d, %v", count, err)
	}
	if count, err := device.BulkRead(context.Background(), 0x81, make([]byte, 8)); err != nil || count != 3 {
		t.Fatalf("BulkRead = %d, %v", count, err)
	}
	if native.writeRef != 4 || native.readRef != 7 {
		t.Fatalf("pipe refs = write %d, read %d", native.writeRef, native.readRef)
	}
	if native.writeTimeout != 5000 || native.readTimeout != 5000 {
		t.Fatalf("timeouts = write %d, read %d", native.writeTimeout, native.readTimeout)
	}
}

func TestDarwinBulkRejectsInvalidEndpoints(t *testing.T) {
	native := &fakeDarwinBulkInterface{}
	device := &Device{
		iface: native,
		routes: map[uint8]darwinPipe{
			0x01: {endpoint: 0x01, ref: 1, transferType: 3},
			0x82: {endpoint: 0x82, ref: 2, transferType: 3},
		},
	}
	tests := []struct {
		name  string
		write bool
		ep    uint8
	}{
		{name: "write to IN", write: true, ep: 0x82},
		{name: "read from OUT", ep: 0x01},
		{name: "unknown OUT", write: true, ep: 0x03},
		{name: "unknown IN", ep: 0x83},
		{name: "non-bulk OUT", write: true, ep: 0x01},
		{name: "non-bulk IN", ep: 0x82},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.write {
				_, err = device.BulkWrite(context.Background(), test.ep, nil)
			} else {
				_, err = device.BulkRead(context.Background(), test.ep, nil)
			}
			if err == nil {
				t.Fatal("bulk transfer succeeded")
			}
		})
	}
	if native.reads != 0 || native.writes != 0 {
		t.Fatalf("native calls = reads %d, writes %d", native.reads, native.writes)
	}
}

func TestDarwinBulkReturnsZeroOnNativeErrors(t *testing.T) {
	want := errors.New("bulk failed")
	native := &fakeDarwinBulkInterface{
		readCount: 4,
		readErr:   want,
		writeErr:  want,
	}
	device := &Device{
		iface: native,
		routes: map[uint8]darwinPipe{
			0x02: {ref: 2, transferType: darwinBulkPipe},
			0x81: {ref: 1, transferType: darwinBulkPipe},
		},
	}
	if count, err := device.BulkWrite(context.Background(), 0x02, nil); count != 0 || !errors.Is(err, want) {
		t.Fatalf("BulkWrite = %d, %v", count, err)
	}
	if count, err := device.BulkRead(context.Background(), 0x81, nil); count != 0 || !errors.Is(err, want) {
		t.Fatalf("BulkRead = %d, %v", count, err)
	}
}

func TestDarwinBulkSendsZeroLengthTransfers(t *testing.T) {
	native := &fakeDarwinBulkInterface{}
	device := &Device{
		iface: native,
		routes: map[uint8]darwinPipe{
			0x02: {ref: 2, transferType: darwinBulkPipe},
			0x81: {ref: 1, transferType: darwinBulkPipe},
		},
	}
	if count, err := device.BulkWrite(context.Background(), 0x02, nil); err != nil || count != 0 {
		t.Fatalf("BulkWrite = %d, %v", count, err)
	}
	if count, err := device.BulkRead(context.Background(), 0x81, nil); err != nil || count != 0 {
		t.Fatalf("BulkRead = %d, %v", count, err)
	}
	if native.reads != 1 || native.writes != 1 {
		t.Fatalf("native calls = reads %d, writes %d", native.reads, native.writes)
	}
}

func TestDarwinBulkValidatesContextAndSize(t *testing.T) {
	if err := validateDarwinBulkLength(uint64(math.MaxUint32) + 1); err == nil {
		t.Fatal("oversized bulk length succeeded")
	}
	native := &fakeDarwinBulkInterface{}
	device := &Device{
		iface: native,
		routes: map[uint8]darwinPipe{
			0x02: {ref: 2, transferType: darwinBulkPipe},
		},
	}
	var nilContext context.Context
	if _, err := device.BulkWrite(nilContext, 0x02, nil); err == nil {
		t.Fatal("BulkWrite(nil) succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := device.BulkWrite(ctx, 0x02, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("BulkWrite(canceled) error = %v", err)
	}
	if native.writes != 0 {
		t.Fatalf("native writes = %d, want 0", native.writes)
	}
}
