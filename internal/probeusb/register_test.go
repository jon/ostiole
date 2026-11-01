package probeusb

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/usb"
)

type registerSessionFake struct {
	*cleanupAttachment
	transport dap.RegisterTransport
	borrows   int
}

func (s *registerSessionFake) RestorationPossible() bool { return s.pending && !s.restorationLost }

func (s *registerSessionFake) Registers() dap.RegisterTransport {
	s.borrows++
	return s.transport
}

func TestRegisterActivationTransfersCleanupOwnership(t *testing.T) {
	for _, transferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "attachment", true: "session"}[transferred], func(t *testing.T) {
			primary, cleanup := errors.New("activation failed"), errors.New("live close failed")
			device := &cleanupAttachment{pending: true, attachmentFake: attachmentFake{err: cleanup}}
			session := &registerSessionFake{cleanupAttachment: &cleanupAttachment{pending: true, attachmentFake: attachmentFake{err: cleanup}}}
			calls := 0
			backend := &registerOwner{state: owner{device: device}, activate: func(context.Context, *usb.Device, probe.SWDConfig) (RegisterSession, error) {
				calls++
				if transferred {
					return session, primary
				}
				return nil, primary
			}}
			p := probe.New(probe.Info{}, backend)
			if !p.SupportsRegisterSWD() || calls != 0 {
				t.Fatal("capability query activated backend")
			}
			_, err := p.RegisterSWD(t.Context(), probe.SWDConfig{MaxClockHz: 1000})
			if !errors.Is(err, primary) || !errors.Is(err, cleanup) || !p.CleanupPending() || calls != 1 {
				t.Fatalf("activation = %v, calls=%d", err, calls)
			}
			retained := device
			if transferred {
				retained = session.cleanupAttachment
				if device.closes != 0 || session.borrows != 1 {
					t.Fatal("session transfer left attachment ownership")
				}
			} else if session.borrows != 0 {
				t.Fatal("borrowed absent session")
			}
			finishRegisterCleanup(t, p, retained, cleanup)
		})
	}
}

func finishRegisterCleanup(t *testing.T, p *probe.Probe, retained *cleanupAttachment, cleanup error) {
	t.Helper()
	if retained.closes != 1 {
		t.Fatal("activation did not attempt cleanup once")
	}
	retained.release = true
	if err := p.Close(); !errors.Is(err, cleanup) || p.CleanupPending() {
		t.Fatalf("final cleanup = %v", err)
	}
	if err := p.Close(); err != nil || retained.closes != 2 {
		t.Fatalf("replayed final error: %v", err)
	}
}

func TestRegisterActivationRetainsSessionWithNoSurface(t *testing.T) {
	for _, typedNil := range []bool{false, true} {
		session := &registerSessionFake{cleanupAttachment: &cleanupAttachment{pending: true, release: true}}
		if typedNil {
			session.transport = (*absentRegisters)(nil)
		}
		device := &attachmentFake{}
		backend := &registerOwner{state: owner{device: device}, activate: func(context.Context, *usb.Device, probe.SWDConfig) (RegisterSession, error) { return session, nil }}
		p := probe.New(probe.Info{}, backend)
		if _, err := p.RegisterSWD(t.Context(), probe.SWDConfig{MaxClockHz: 1000}); err == nil {
			t.Fatal("accepted absent register surface")
		}
		if session.closes != 1 || session.borrows != 1 || device.closes != 0 || p.CleanupPending() {
			t.Fatal("absent surface lost transferred cleanup")
		}
		if err := p.Close(); err != nil || session.closes != 1 {
			t.Fatal("repeated final cleanup")
		}
	}
}

type absentRegisters struct{ dap.RegisterTransport }

func TestRegisterActivationSuccessTransfersFinalHostError(t *testing.T) {
	final := errors.New("final host error")
	device := &attachmentFake{}
	session := &registerSessionFake{cleanupAttachment: &cleanupAttachment{pending: true, release: true, attachmentFake: attachmentFake{err: final}}, transport: &absentRegisters{}}
	calls := 0
	backend := &registerOwner{state: owner{device: device}, activate: func(context.Context, *usb.Device, probe.SWDConfig) (RegisterSession, error) {
		calls++
		return session, nil
	}}
	p := probe.New(probe.Info{}, backend)
	if _, err := p.RegisterSWD(t.Context(), probe.SWDConfig{MaxClockHz: 1000}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || session.borrows != 1 || device.closes != 0 || session.closes != 0 || !p.CleanupPending() {
		t.Fatal("activation changed host ownership incorrectly")
	}
	session.restorationLost = true
	if p.RestorationPossible() || !p.CleanupPending() {
		t.Fatal("terminal loss abandoned pending host cleanup")
	}
	if err := p.Close(); !errors.Is(err, final) || p.CleanupPending() {
		t.Fatalf("final close=%v", err)
	}
	if err := p.Close(); err != nil || session.closes != 1 || device.closes != 0 {
		t.Fatalf("replayed host cleanup: %v", err)
	}
}
