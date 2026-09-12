package dap

import (
	"context"
	"errors"

	"github.com/jon/ostiole/swd"
)

type transferOutcome uint8

const (
	transferUnsent transferOutcome = iota
	transferRejected
	transferConfirmed
	transferIndeterminate
)

type transferResult struct {
	data    uint32
	cause   error
	outcome transferOutcome
}

func (r transferResult) value() (uint32, error) { return r.data, r.cause }

func (r transferResult) err() error { return r.cause }

type swdExecutor struct {
	*swd.Conn

	dp *DebugPort
}

func newSWDExecutor(conn *swd.Conn, dp *DebugPort) *swdExecutor {
	if conn == nil {
		return nil
	}
	return &swdExecutor{Conn: conn, dp: dp}
}

func (e *swdExecutor) Connect(ctx context.Context) (uint32, error) {
	value, err := e.Conn.Connect(ctx)
	return value, classifyPortError(err)
}

func (e *swdExecutor) Release(ctx context.Context) error {
	return classifyPortError(e.Conn.Release(ctx))
}

func (e *swdExecutor) exchange(ctx context.Context, req transferRequest, data uint32) (uint32, error) {
	if req.AP && req.Read {
		return e.ReadAP(ctx, req.Addr)
	}
	if req.AP {
		return 0, e.WriteAP(ctx, req.Addr, data)
	}
	if req.Read {
		return e.ReadDP(ctx, req.Addr)
	}
	return 0, e.WriteDP(ctx, req.Addr, data)
}

type swdQueuedResult struct {
	read  *swd.ReadResult
	write *swd.WriteResult
}

func (r swdQueuedResult) completed() transferResult {
	if r.read != nil {
		return swdResult(r.read.Value())
	}
	return swdResult(0, r.write.Err())
}

func swdResult(value uint32, err error) transferResult {
	outcome := transferIndeterminate
	switch {
	case err == nil || err == swd.ErrParity:
		outcome = transferConfirmed
	case errors.Is(err, swd.ErrNotExecuted):
		outcome = transferUnsent
	case err == swd.ErrWait || err == swd.ErrFault:
		outcome = transferRejected
	}
	return transferResult{data: value, cause: err, outcome: outcome}
}

func (e *swdExecutor) transferSteps(ctx context.Context, steps []txnStep) ([]transferResult, error) {
	batch := e.NewBatch()
	queued := make([]swdQueuedResult, len(steps))
	for i, step := range steps {
		switch {
		case step.req.AP && step.req.Read:
			queued[i].read = batch.ReadAP(step.req.Addr)
		case step.req.AP:
			queued[i].write = batch.WriteAP(step.req.Addr, step.data)
		case step.req.Read:
			queued[i].read = batch.ReadDP(step.req.Addr)
		default:
			queued[i].write = batch.WriteDP(step.req.Addr, step.data)
		}
	}
	err := batch.Commit(ctx)
	results := make([]transferResult, len(steps))
	for i := range queued {
		results[i] = queued[i].completed()
	}
	return results, err
}
