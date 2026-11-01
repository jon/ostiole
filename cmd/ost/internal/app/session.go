package app

import (
	"context"
	"errors"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/discover"
	ftdidiscovery "github.com/jon/ostiole/ftdi/discovery"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/swd"
)

type probeOpener func(context.Context) (*probe.Probe, error)

type swdSession struct {
	opened     *probe.Probe
	connection *swd.Conn
}

type dapSession struct {
	owner    *armdebug.Conn
	opened   *probe.Probe
	port     *dap.DebugPort
	identity dap.DPIDRInfo
	memory   *dap.MemAP
}

func openFTDIProbe(ctx context.Context) (*probe.Probe, error) {
	return discover.OpenProbe(ctx, discover.Selection{Provider: ftdidiscovery.ID, Function: "A"})
}

func openSWDWith(ctx context.Context, open probeOpener) (*swdSession, error) {
	opened, err := open(ctx)
	if opened == nil {
		if err == nil {
			err = errors.New("ost: no opened probe")
		}
		return nil, err
	}
	session := &swdSession{opened: opened}
	if err != nil {
		return session, err
	}
	wire, err := opened.SWD(ctx, probe.SWDConfig{MaxClockHz: 1_000_000})
	if err != nil {
		return session, err
	}
	session.connection = swd.New(wire)
	_, err = session.connection.Connect(ctx)
	return session, err
}

func (s *swdSession) close() error {
	if s == nil || s.opened == nil {
		return nil
	}
	err := s.releaseProtocol()
	if err != nil && s.connection.CleanupPending() && s.opened.RestorationPossible() {
		return err
	}
	s.connection = nil
	hostErr := s.opened.Close()
	if !s.opened.CleanupPending() {
		s.opened = nil
	}
	return errors.Join(err, hostErr)
}

func (s *swdSession) releaseProtocol() error {
	if !s.connection.CleanupPending() {
		return nil
	}
	if !s.opened.RestorationPossible() {
		return armdebug.ErrRestorationAbandoned
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := s.connection.Release(ctx)
	cancel()
	if err != nil && !s.opened.RestorationPossible() {
		return errors.Join(err, armdebug.ErrRestorationAbandoned)
	}
	return err
}

func openDAPWith(ctx context.Context, open probeOpener) (*dapSession, error) {
	opened, err := open(ctx)
	if opened == nil {
		if err == nil {
			err = errors.New("ost: no opened probe")
		}
		return nil, err
	}
	if err != nil {
		return &dapSession{opened: opened}, err
	}
	owner, err := armdebug.Connect(ctx, opened, armdebug.Config{
		Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 1_000_000}),
	})
	if owner == nil {
		return nil, err
	}
	session := &dapSession{owner: owner, port: owner.Port()}
	if err != nil {
		return session, err
	}
	value, err := session.port.ReadDP(ctx, dap.DPIDR)
	if err != nil {
		return session, err
	}
	session.identity, err = dap.DecodeDPIDR(value)
	return session, err
}

func (s *dapSession) close() error {
	if s == nil {
		return nil
	}
	if s.owner != nil {
		return s.owner.Close()
	}
	if s.opened == nil {
		return nil
	}
	err := s.opened.Close()
	if !s.opened.CleanupPending() {
		s.opened = nil
	}
	return err
}
