//go:build linux

package usb

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
)

type cachedConfigurationFixture struct {
	*Device
	configs       [][]byte
	active        uint8
	largeRequests int
}

func (f *cachedConfigurationFixture) ControlTransfer(_ context.Context, kind, request uint8, value, index uint16, data []byte) (int, error) {
	if kind != 0x80 || index != 0 {
		return 0, syscall.EINVAL
	}
	if len(data) > os.Getpagesize() {
		f.largeRequests++
		return 0, syscall.EINVAL
	}
	switch {
	case request == 8:
		return copy(data, []byte{f.active}), nil
	case request == 6 && value == 0x100:
		raw := make([]byte, 18)
		raw[0], raw[1], raw[17] = 18, 1, byte(len(f.configs))
		return copy(data, raw), nil
	case request == 6 && value>>8 == 2 && int(value&255) < len(f.configs):
		return copy(data, f.configs[int(value&255)]), nil
	default:
		return 0, syscall.EINVAL
	}
}

func configurationFixture(t *testing.T, configs [][]byte, active uint8) *cachedConfigurationFixture {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "selected-usb-descriptors")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	raw := make([]byte, 18)
	raw[0], raw[1], raw[17] = 18, 1, byte(len(configs))
	for _, config := range configs {
		raw = append(raw, config...)
	}
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
	return &cachedConfigurationFixture{Device: &Device{file: file}, configs: configs, active: active}
}

func largeUSBConfiguration(t *testing.T) []byte {
	t.Helper()
	if os.Getpagesize()+1024 > 65535 {
		t.Skip("host page size reaches USB descriptor length limit")
	}
	raw := singleInterfaceConfiguration(7)
	for len(raw) < os.Getpagesize()+1024 {
		raw = append(raw, 2, 0x24)
	}
	binary.LittleEndian.PutUint16(raw[2:4], uint16(len(raw)))
	return raw
}

func TestLinuxConfigurationUsesSelectedAttachmentDescriptors(t *testing.T) {
	first, second := singleInterfaceConfiguration(1), largeUSBConfiguration(t)
	f := configurationFixture(t, [][]byte{first, second}, 7)
	config, err := activeConfiguration(t.Context(), f)
	if err != nil || config.Value != 7 || len(config.Interfaces) != 1 || f.largeRequests != 0 {
		t.Fatalf("large selected configuration: %+v %v, largecontrol%d", config, err, f.largeRequests)
	}
	if len(config.Interfaces[0].Alternates) != 1 {
		t.Fatal("lost interface alternate")
	}
}

func TestLinuxConfigurationRejectsMalformedAttachmentData(t *testing.T) {
	for _, which := range []string{"short-header", "short-body", "zero-length", "wrong-type", "changed-value", "changed-length", "shorter-valid"} {
		t.Run(which, func(t *testing.T) {
			f := configurationFixture(t, [][]byte{singleInterfaceConfiguration(1), largeUSBConfiguration(t)}, 7)
			if err := changeCachedDescriptor(f, which); err != nil {
				t.Fatal(err)
			}
			if _, err := activeConfiguration(t.Context(), f); err == nil {
				t.Fatal("accepted malformed selected-attachment descriptors")
			}
			if f.largeRequests != 0 {
				t.Fatal("replayed through control path after descriptor failure")
			}
		})
	}
}

func TestLinuxConfigurationPreservesDescriptorReadAndCancellationErrors(t *testing.T) {
	f := configurationFixture(t, [][]byte{largeUSBConfiguration(t)}, 7)
	if err := f.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := activeConfiguration(t.Context(), f); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed descriptor source: %v", err)
	}
	f = configurationFixture(t, [][]byte{largeUSBConfiguration(t)}, 7)
	if err := f.file.Truncate(18 + 10); err != nil {
		t.Fatal(err)
	}
	if _, err := activeConfiguration(t.Context(), f); !errors.Is(err, io.EOF) {
		t.Fatalf("truncated descriptor source: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := activeConfiguration(ctx, f); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inspection: %v", err)
	}
}

func changeCachedDescriptor(f *cachedConfigurationFixture, which string) error {
	offset := int64(18 + len(f.configs[0]))
	var data []byte
	switch which {
	case "short-header":
		return f.file.Truncate(offset + 4)
	case "short-body":
		return f.file.Truncate(offset + 20)
	case "zero-length":
		offset, data = 20, []byte{0, 0}
	case "wrong-type":
		offset, data = 19, []byte{3}
	case "changed-value":
		offset, data = offset+5, []byte{9}
	case "changed-length":
		offset, data = offset+2, []byte{9, 0}
	case "shorter-valid":
		data = singleInterfaceConfiguration(7)
	default:
		return errors.New("unknown descriptor corruption")
	}
	_, err := f.file.WriteAt(data, offset)
	return err
}

type failingDescriptorReader struct {
	reader          io.ReaderAt
	failCall, calls int
	err             error
}

func (r *failingDescriptorReader) ReadAt(data []byte, offset int64) (int, error) {
	r.calls++
	if r.calls == r.failCall {
		return 0, &os.PathError{Op: "read", Path: "selected-attachment", Err: r.err}
	}
	return r.reader.ReadAt(data, offset)
}

func TestLinuxCachedDescriptorReadClassifiesDeviceLoss(t *testing.T) {
	for _, failCall := range []int{1, 2} {
		for _, native := range []error{syscall.ENODEV, syscall.EIO, syscall.EACCES} {
			f := configurationFixture(t, [][]byte{singleInterfaceConfiguration(7)}, 7)
			reader := &failingDescriptorReader{reader: f.file, failCall: failCall, err: native}
			_, err := readAttachmentConfiguration(t.Context(), reader, 0, len(f.configs[0]))
			if !errors.Is(err, native) || errors.Is(err, ErrDeviceGone) != (native == syscall.ENODEV) {
				t.Errorf("read %d native %v: %v", failCall, native, err)
			}
			if reader.calls != failCall {
				t.Fatalf("read failure replayed: %d calls, want %d", reader.calls, failCall)
			}
		}
	}
}
