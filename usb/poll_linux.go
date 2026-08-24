//go:build linux

package usb

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const (
	linuxPollIn      = 0x0001
	linuxPollOut     = 0x0004
	linuxPollError   = 0x0008
	linuxPollHangup  = 0x0010
	linuxPollInvalid = 0x0020
)

var errLinuxBulkPollerStopped = errors.New("usb: bulk completion poller stopped")

var errLinuxBulkPollerTerminal = errors.New("usb: bulk completion file descriptor is unavailable")

type linuxBulkPoller interface {
	wait() error
	stopWaiting() error
	close() error
}

type usbfsBulkPoller struct {
	deviceFD uintptr
	reader   *os.File
	writer   *os.File
	stopOnce sync.Once
	stopErr  error
}

type linuxPollFD struct {
	fd      int32
	events  int16
	revents int16
}

func newUSBFSBulkPoller(deviceFD uintptr) (linuxBulkPoller, error) {
	if deviceFD > math.MaxInt32 {
		return nil, fmt.Errorf("usb: device file descriptor %d exceeds poll limit", deviceFD)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("usb: create bulk completion wake pipe: %w", err)
	}
	return &usbfsBulkPoller{deviceFD: deviceFD, reader: reader, writer: writer}, nil
}

func (p *usbfsBulkPoller) wait() error {
	fds := [2]linuxPollFD{
		{fd: int32(p.deviceFD), events: linuxPollOut},
		{fd: int32(p.reader.Fd()), events: linuxPollIn},
	}
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&fds[0])), uintptr(len(fds)), 0, 0, 0, 0)
		runtime.KeepAlive(p)
		if errors.Is(errno, syscall.EINTR) {
			continue
		}
		if errno != 0 {
			return errno
		}
		if fds[1].revents&(linuxPollIn|linuxPollError|linuxPollHangup|linuxPollInvalid) != 0 {
			return errLinuxBulkPollerStopped
		}
		if fds[0].revents&linuxPollInvalid != 0 {
			return syscall.EBADF
		}
		if fds[0].revents&(linuxPollError|linuxPollHangup) != 0 {
			return errLinuxBulkPollerTerminal
		}
		if fds[0].revents&linuxPollOut != 0 {
			return nil
		}
	}
}

func (p *usbfsBulkPoller) stopWaiting() error {
	p.stopOnce.Do(func() {
		_, p.stopErr = p.writer.Write([]byte{1})
	})
	return p.stopErr
}

func (p *usbfsBulkPoller) close() error {
	return errors.Join(p.reader.Close(), p.writer.Close())
}
