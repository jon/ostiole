package dap

import (
	"context"
	"errors"
)

func (dp *DebugPort) failRegisterConnect(cause error) error {
	if !dp.conn.CleanupPending() {
		dp.completeRelease()
		return cause
	}
	ctx, cancel := dp.cleanupContext()
	defer cancel()
	err := dp.conn.Release(ctx)
	if !dp.conn.CleanupPending() {
		dp.completeRelease()
	} else {
		dp.state.loseFraming()
	}
	return errors.Join(cause, err)
}

func (e *swdExecutor) releaseRegister(ctx context.Context) error {
	err := e.register.Release(ctx)
	if err == nil && e.register.CleanupPending() {
		return errors.Join(ErrProtocol, errors.New("dap: register transport retained cleanup after successful release"))
	}
	return err
}
