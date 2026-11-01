package probeusb

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

type cleanupAttachment struct {
	attachmentFake
	pending         bool
	release         bool
	restorationLost bool
}

func (a *cleanupAttachment) CleanupPending() bool { return a.pending }
func (a *cleanupAttachment) Close() error {
	if a.release {
		a.pending = false
	}
	return a.attachmentFake.Close()
}

type cleanupSession struct{ *cleanupAttachment }

func (*cleanupSession) SWDIO(context.Context, []byte, []byte, int) ([]byte, error) { return nil, nil }
func (*cleanupSession) MaxTransferBits() int                                       { return 64 }

func TestProbeReportsFinalCleanupErrorOnlyOnce(t *testing.T) {
	for _, activated := range []bool{false, true} {
		t.Run(map[bool]string{false: "attachment", true: "session"}[activated], func(t *testing.T) {
			want := errors.New("cached final host-close failure")
			a := &cleanupAttachment{attachmentFake: attachmentFake{err: want}, pending: true, release: true}
			o := &owner{}
			if activated {
				o.session = &cleanupSession{a}
			} else {
				o.device = a
			}
			p := probe.New(probe.Info{}, o)
			if err := p.Close(); !errors.Is(err, want) {
				t.Fatalf("first Close() = %v", err)
			}
			if err := p.Close(); err != nil {
				t.Fatalf("final error repeated after host release: %v", err)
			}
			if a.closes != 1 || o.device != nil || o.session != nil {
				t.Fatalf("released resource retained or retried: closes=%d owner=%#v", a.closes, o)
			}
		})
	}
}

func (s *cleanupSession) RestorationPossible() bool { return s.pending && !s.restorationLost }

func TestProbeUSBStateTracksLiveRetryAndFinalRelease(t *testing.T) {
	for _, activated := range []bool{false, true} {
		want := errors.New("live cleanup failure")
		a := &cleanupAttachment{attachmentFake: attachmentFake{err: want}, pending: true}
		o := &owner{}
		if activated {
			o.session = &cleanupSession{a}
		} else {
			o.device = a
		}
		p := probe.New(probe.Info{}, o)
		if !p.CleanupPending() || !p.RestorationPossible() || a.closes != 0 {
			t.Fatal("state query lost resources or performed close")
		}
		if activated {
			a.restorationLost = true
			if p.RestorationPossible() || !p.CleanupPending() {
				t.Fatal("terminal session state confused with host release")
			}
		}
		if err := p.Close(); !errors.Is(err, want) || !p.CleanupPending() {
			t.Fatalf("live failure = %v", err)
		}
		a.release = true
		if err := p.Close(); !errors.Is(err, want) || p.CleanupPending() || p.RestorationPossible() {
			t.Fatalf("final release = %v", err)
		}
		if err := p.Close(); err != nil || a.closes != 2 {
			t.Fatalf("final release repeated: %v, closes=%d", err, a.closes)
		}
	}
}

func TestActivationFinalCleanupPreservesBothErrors(t *testing.T) {
	for _, activated := range []bool{false, true} {
		primary, final := errors.New("activation failed"), errors.New("final cleanup error")
		a := &cleanupAttachment{attachmentFake: attachmentFake{err: final}, pending: true, release: true}
		o := &owner{device: a, activate: func(context.Context, *usb.Device, probe.SWDConfig) (Session, error) {
			if activated {
				return &cleanupSession{a}, primary
			}
			return nil, primary
		}}
		p := probe.New(probe.Info{}, o)
		_, err := p.SWD(t.Context(), probe.SWDConfig{MaxClockHz: 1000})
		if !errors.Is(err, primary) || !errors.Is(err, final) || p.CleanupPending() {
			t.Fatalf("activation final cleanup = %v, pending=%t", err, p.CleanupPending())
		}
		if err := p.Close(); err != nil || a.closes != 1 {
			t.Fatalf("final activation cleanup retried: %v, closes=%d", err, a.closes)
		}
	}
}
