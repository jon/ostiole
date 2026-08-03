//go:build darwin && cgo

package usb

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type fakeDarwinControlDevice struct {
	fakeDarwinDevice
	request darwinControlRequest
	data    []byte
	count   uint16
	err     error
	calls   int
}

func (f *fakeDarwinControlDevice) control(request darwinControlRequest, data []byte) (uint16, error) {
	f.calls++
	f.request = request
	f.data = data
	return f.count, f.err
}

func TestDarwinControlTransferRoutesRequestAndReturnsNativeCount(t *testing.T) {
	native := &fakeDarwinControlDevice{count: 2}
	device := &Device{handle: native}
	data := []byte{0xaa, 0xbb}
	count, err := device.ControlTransfer(context.Background(), 0x40, 0x0b, 0x0200, 1, data)
	if err != nil || count != 2 {
		t.Fatalf("ControlTransfer = %d, %v", count, err)
	}
	want := darwinControlRequest{
		requestType: 0x40,
		request:     0x0b,
		value:       0x0200,
		index:       1,
		timeout:     5000,
	}
	if native.request != want {
		t.Fatalf("native request = %#v, want %#v", native.request, want)
	}
	if len(native.data) != 2 || &native.data[0] != &data[0] {
		t.Fatal("native control did not receive the caller's buffer")
	}
}

func TestDarwinControlTransferReturnsZeroOnNativeFailure(t *testing.T) {
	want := errors.New("control failed")
	native := &fakeDarwinControlDevice{count: 3, err: want}
	device := &Device{handle: native}
	count, err := device.ControlTransfer(context.Background(), 0x80, 1, 2, 3, make([]byte, 4))
	if count != 0 || !errors.Is(err, want) {
		t.Fatalf("ControlTransfer = %d, %v", count, err)
	}
}

func TestDarwinControlTransferSendsZeroLengthRequest(t *testing.T) {
	native := &fakeDarwinControlDevice{}
	device := &Device{handle: native}
	count, err := device.ControlTransfer(context.Background(), 0x40, 1, 2, 3, nil)
	if err != nil || count != 0 || native.calls != 1 || native.data != nil {
		t.Fatalf("ControlTransfer = %d, %v; native = %#v", count, err, native)
	}
}

func TestDarwinControlTransferRejectsAClosedDevice(t *testing.T) {
	native := &fakeDarwinControlDevice{}
	device := &Device{handle: native}
	if err := device.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := device.ControlTransfer(context.Background(), 0, 0, 0, 0, nil); !errors.Is(err, errDarwinDeviceClosed) {
		t.Fatalf("ControlTransfer error = %v, want %v", err, errDarwinDeviceClosed)
	}
	if native.calls != 0 {
		t.Fatalf("native control calls = %d, want 0", native.calls)
	}
}

func TestDarwinControlTransferValidatesContextAndLength(t *testing.T) {
	native := &fakeDarwinControlDevice{}
	device := &Device{handle: native}
	var nilContext context.Context
	if _, err := device.ControlTransfer(nilContext, 0, 0, 0, 0, nil); err == nil {
		t.Fatal("ControlTransfer(nil) succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := device.ControlTransfer(ctx, 0, 0, 0, 0, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ControlTransfer(canceled) error = %v", err)
	}
	if _, err := device.ControlTransfer(context.Background(), 0, 0, 0, 0, make([]byte, 1<<16)); err == nil {
		t.Fatal("oversized ControlTransfer succeeded")
	}
	if native.calls != 0 {
		t.Fatalf("native calls = %d, want 0", native.calls)
	}
}

func TestDarwinTransferTimeoutRoundsUpMilliseconds(t *testing.T) {
	now := time.Unix(100, 0)
	tests := []struct {
		name string
		ctx  context.Context
		want uint32
		err  error
	}{
		{name: "default", ctx: context.Background(), want: 5000},
		{
			name: "rounded",
			ctx: deadlineContext{
				Context:  context.Background(),
				deadline: now.Add(1100 * time.Microsecond),
			},
			want: 2,
		},
		{
			name: "expired",
			ctx: deadlineContext{
				Context:  context.Background(),
				deadline: now,
			},
			err: context.DeadlineExceeded,
		},
		{
			name: "native maximum",
			ctx: deadlineContext{
				Context:  context.Background(),
				deadline: now.Add(time.Duration(math.MaxInt64)),
			},
			want: math.MaxUint32,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := darwinTransferTimeout(test.ctx, now)
			if got != test.want || !errors.Is(err, test.err) {
				t.Fatalf("darwinTransferTimeout = %d, %v", got, err)
			}
		})
	}
}

type deadlineContext struct {
	context.Context
	deadline time.Time
}

func (c deadlineContext) Deadline() (time.Time, bool) {
	return c.deadline, true
}
