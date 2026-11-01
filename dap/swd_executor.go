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
	transferInFlight
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
	dp          *DebugPort
	register    RegisterTransport
	recovering  bool
	pendingRead bool
}

func newSWDExecutor(conn *swd.Conn, dp *DebugPort) *swdExecutor {
	if conn == nil {
		return nil
	}
	return &swdExecutor{Conn: conn, dp: dp}
}

func (e *swdExecutor) Connect(ctx context.Context) (uint32, error) {
	if e.register != nil {
		return e.register.Connect(ctx)
	}
	value, err := e.Conn.Connect(ctx, swd.WithCleanupTimeout(e.dp.cleanupTimeout))
	return value, classifyPortError(err)
}

func (e *swdExecutor) Release(ctx context.Context) error {
	if e.register != nil {
		return e.releaseRegister(ctx)
	}
	return classifyPortError(e.Conn.Release(ctx))
}

func (e *swdExecutor) exchange(ctx context.Context, req transferRequest, data uint32) (uint32, error) {
	if e.register != nil {
		r := e.registerExchange(ctx, req, data)
		return r.data, r.cause
	}
	batch := e.NewBatch()
	var read *swd.ReadResult
	var write *swd.WriteResult
	switch {
	case req.AP && req.Read:
		read = batch.ReadAP(req.Addr)
	case req.AP:
		write = batch.WriteAP(req.Addr, data)
	case req.Read:
		read = batch.ReadDP(req.Addr)
	default:
		write = batch.WriteDP(req.Addr, data)
	}
	// Commit reports cancellation separately from the physical result. The
	// logical owner records that result before reporting caller cancellation.
	commitErr := batch.Commit(ctx)
	var value uint32
	var err error
	if read != nil {
		value, err = read.Value()
	} else {
		err = write.Err()
	}
	if errors.Is(err, swd.ErrNotExecuted) {
		err = errors.Join(err, commitErr)
	}
	return value, err
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

func (t *Txn) recordSWDCompletions() {
	for i := range t.ops {
		op := &t.ops[i]
		if op.kind != txnWriteAPSequence || !op.result.resolved {
			continue
		}
		err := op.result.err
		if err == nil || op.accepted == len(op.values) && errors.Is(err, swd.ErrParity) && !errors.Is(err, ErrIndeterminate) {
			op.confirmed = len(op.values)
		}
		op.uncertainWrite = op.accepted > op.confirmed && (len(op.values) != 1 || !faultReportsWriteDataError(err))
	}
}

type swdTxn struct{ *Txn }

func (e *swdExecutor) executeTxn(ctx context.Context, txn *Txn) error {
	defer txn.recordSWDCompletions()
	if err := e.dp.settlePreviousDPWrite(ctx); err != nil {
		txn.resolveSuffix(0)
		return err
	}
	err := (&swdTxn{Txn: txn}).execute(ctx, newSWDTxnPlanner(e.dp).plan(txn.ops))
	for i := range txn.ops {
		result := txn.ops[i].result
		result.err = classifyPortError(result.err)
	}
	return classifyPortError(err)
}

func (e *swdExecutor) CleanupPending() bool {
	if e.register != nil {
		return e.register.CleanupPending()
	}
	return e.Conn.CleanupPending()
}
