package probeusb

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

func TestJTAGActivationRetainsSession(t *testing.T) {
	d := &attachmentFake{}
	s := &jtagSessionFake{attachmentFake: attachmentFake{err: errors.New("cleanup")}}
	primary := errors.New("activate")
	b := &jtagOwner{owner: &owner{device: d}, activateJTAG: func(context.Context, *usb.Device, probe.JTAGConfig) (JTAGSession, error) {
		return s, primary
	}}
	p := probe.New(probe.Info{}, b)
	_, err := p.JTAG(t.Context(), probe.JTAGConfig{MaxClockHz: 1000})
	if !errors.Is(err, primary) || !errors.Is(err, s.err) || d.closes != 0 || s.closes != 1 {
		t.Fatalf("lost session ownership: %v", err)
	}
	s.err = nil
	if err := p.Close(); err != nil || s.closes != 2 || d.closes != 0 {
		t.Fatal("session cleanup not retried")
	}
}

type jtagSessionFake struct{ attachmentFake }

func (*jtagSessionFake) JTAGIO(context.Context, []byte, []byte, int) ([]byte, error) { return nil, nil }
func (*jtagSessionFake) MaxTransferBits() int                                        { return 64 }

func TestNilJTAGSessionFailureRetainsAttachmentForCleanupRetry(t *testing.T) {
	primary, cleanup := errors.New("activate"), errors.New("cleanup")
	d := &attachmentFake{err: cleanup}
	calls := 0
	b := &jtagOwner{owner: &owner{device: d}, activateJTAG: func(context.Context, *usb.Device, probe.JTAGConfig) (JTAGSession, error) {
		calls++
		return nil, primary
	}}
	p := probe.New(probe.Info{}, b)
	if _, err := p.JTAG(t.Context(), probe.JTAGConfig{MaxClockHz: 1000}); !errors.Is(err, primary) || !errors.Is(err, cleanup) || d.closes != 1 || !p.CleanupPending() {
		t.Fatalf("nil session cleanup: %v", err)
	}
	if _, err := p.JTAG(t.Context(), probe.JTAGConfig{MaxClockHz: 1000}); err == nil || calls != 1 {
		t.Fatal("replayed failed activation")
	}
	d.err = nil
	if err := p.Close(); err != nil || d.closes != 2 || p.CleanupPending() {
		t.Fatalf("attachment retry: %v", err)
	}
}
