//go:build linux

package usb

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestUSBFSBulkPollerReportsWritableDescriptor(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	closeLinuxTestFile(t, reader)
	closeLinuxTestFile(t, writer)
	poller, err := newUSBFSBulkPoller(writer.Fd())
	if err != nil {
		t.Fatal(err)
	}
	closeLinuxTestPoller(t, poller)
	if err := poller.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestUSBFSBulkPollerCanStopBlockedWait(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	closeLinuxTestFile(t, reader)
	closeLinuxTestFile(t, writer)
	poller, err := newUSBFSBulkPoller(reader.Fd())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- poller.wait() }()
	select {
	case err := <-done:
		t.Fatalf("wait returned before stop: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := poller.stopWaiting(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errLinuxBulkPollerStopped) {
			t.Fatalf("wait error = %v, want %v", err, errLinuxBulkPollerStopped)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not stop")
	}
	closeLinuxTestPoller(t, poller)
}

func TestUSBFSBulkPollerReportsTerminalDescriptorState(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	closeLinuxTestFile(t, reader)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	poller, err := newUSBFSBulkPoller(reader.Fd())
	if err != nil {
		t.Fatal(err)
	}
	closeLinuxTestPoller(t, poller)
	if err := poller.wait(); !errors.Is(err, errLinuxBulkPollerTerminal) {
		t.Fatalf("wait error = %v, want %v", err, errLinuxBulkPollerTerminal)
	}
}

func closeLinuxTestFile(t *testing.T, file *os.File) {
	t.Helper()
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
}

func closeLinuxTestPoller(t *testing.T, poller linuxBulkPoller) {
	t.Helper()
	t.Cleanup(func() {
		if err := poller.close(); err != nil {
			t.Error(err)
		}
	})
}
