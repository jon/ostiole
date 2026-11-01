package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type registerModel struct {
	target                  *dapsim.Target
	requests                []swdsim.Request
	connects, releases      int
	active, originalOverrun bool
	failFor                 swdsim.Request
	failStatus              dap.RegisterStatus
	failCause               error
	afterRead               bool
	onCompletion            func()
	afterWrite              bool
	waits                   int
}

func (m *registerModel) Connect(ctx context.Context) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.connects++
	if !m.active {
		m.originalOverrun = m.target.OverrunDetectEnabled()
		m.active = true
	}
	m.target.ObserveLineReset()
	m.target.SetOverrunDetect(false)
	if err := m.target.Write(ctx, dpWrite(0), 0x1e); err != nil {
		return 0, err
	}
	if err := m.target.Write(ctx, dpWrite(8), 0); err != nil {
		return 0, err
	}
	return m.target.Read(ctx, dpRead(0))
}
func (m *registerModel) Release(ctx context.Context) error {
	if !m.active {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.releases++
	m.target.SetOverrunDetect(m.originalOverrun)
	m.active = false
	return nil
}
func (m *registerModel) CleanupPending() bool { return m.active }
func (m *registerModel) ReadDP(ctx context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	value, status, err := m.read(ctx, dpRead(addr))
	if err == nil && m.afterRead && m.failFor == dpRead(addr) && m.failCause != nil {
		err, status = m.failCause, m.failStatus
		m.failCause = nil
		if m.onCompletion != nil {
			m.onCompletion()
		}
	}
	return value, status, err
}
func (m *registerModel) WriteDP(ctx context.Context, addr uint8, value uint32) (dap.RegisterStatus, error) {
	return m.write(ctx, dpWrite(addr), value)
}
func (m *registerModel) accept(ctx context.Context, req swdsim.Request) (dap.RegisterStatus, error) {
	if err := ctx.Err(); err != nil {
		return dap.RegisterUnsent, err
	}
	m.requests = append(m.requests, req)
	if req == m.failFor && m.failCause != nil && !m.afterWrite && !m.afterRead {
		err := m.failCause
		m.failCause = nil
		if m.onCompletion != nil {
			m.onCompletion()
		}
		return m.failStatus, err
	}
	if req == m.failFor && m.waits > 0 {
		m.waits--
		return dap.RegisterRejected, dap.ErrWait
	}
	if err := m.target.Acknowledge(ctx, req); err != nil {
		if errors.Is(err, swd.ErrFault) {
			err = dap.ErrFault
		}
		return dap.RegisterRejected, err
	}
	return dap.RegisterCompleted, nil
}
func (m *registerModel) read(ctx context.Context, req swdsim.Request) (uint32, dap.RegisterStatus, error) {
	status, err := m.accept(ctx, req)
	if err != nil {
		return 0, status, err
	}
	value, err := m.target.Read(ctx, req)
	if err != nil {
		return 0, dap.RegisterIndeterminate, err
	}
	return value, dap.RegisterCompleted, nil
}
func (m *registerModel) write(ctx context.Context, req swdsim.Request, value uint32) (dap.RegisterStatus, error) {
	status, err := m.accept(ctx, req)
	if err != nil {
		return status, err
	}
	if err := m.target.Write(ctx, req, value); err != nil {
		return dap.RegisterIndeterminate, err
	}
	if m.afterWrite && req == m.failFor && m.failCause != nil {
		err := m.failCause
		m.failCause = nil
		return m.failStatus, err
	}
	return dap.RegisterCompleted, nil
}

func newRegisterModel(t *testing.T) *registerModel {
	t.Helper()
	return &registerModel{target: dapsim.New(0x2ba01477)}
}

type registerLifecycle struct {
	*registerModel
	enter func(context.Context) (uint32, error)
	leave func(context.Context) error
}

func (m *registerLifecycle) Connect(ctx context.Context) (uint32, error) {
	if m.enter != nil {
		return m.enter(ctx)
	}
	return m.registerModel.Connect(ctx)
}
func (m *registerLifecycle) Release(ctx context.Context) error {
	if m.leave != nil {
		return m.leave(ctx)
	}
	return m.registerModel.Release(ctx)
}
