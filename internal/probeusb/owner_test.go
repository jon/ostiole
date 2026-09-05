package probeusb

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

func TestActivationTransfersOrRetainsOwnership(t *testing.T) {
	for _, retain := range []bool{false, true} {
		t.Run(map[bool]string{false: "attachment", true: "session"}[retain], func(t *testing.T) {
			primary, cleanup := errors.New("activation"), errors.New("cleanup")
			d := &attachmentFake{err: cleanup}
			s := &sessionFake{attachmentFake: attachmentFake{err: cleanup}}
			b := &owner{device: d, activate: func(context.Context, *usb.Device, probe.SWDConfig) (Session, error) {
				if retain {
					return s, primary
				}
				return nil, primary
			}}
			p := probe.New(probe.Info{}, b)
			_, err := p.SWD(t.Context(), probe.SWDConfig{MaxClockHz: 1000})
			if !errors.Is(err, primary) || !errors.Is(err, cleanup) {
				t.Fatalf("lost error: %v", err)
			}
			if retain && (s.closes != 1 || d.closes != 0) {
				t.Fatal("session ownership lost")
			}
			if !retain && d.closes != 1 {
				t.Fatal("attachment ownership lost")
			}
			d.err, s.err = nil, nil
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type attachmentFake struct {
	closes int
	err    error
}

func (d *attachmentFake) Close() error   { d.closes++; return d.err }
func (*attachmentFake) raw() *usb.Device { return nil }

type sessionFake struct{ attachmentFake }

func (*sessionFake) SWDIO(context.Context, []byte, []byte, int) ([]byte, error) { return nil, nil }
func (*sessionFake) MaxTransferBits() int                                       { return 64 }
