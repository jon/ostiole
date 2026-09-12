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
