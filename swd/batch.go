package swd

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrResultPending reports access to a batch result before Commit.
	ErrResultPending = errors.New("swd: batch result is pending")
	// ErrBatchCommitted reports reuse of a single-use batch.
	ErrBatchCommitted = errors.New("swd: batch is already committed")
	// ErrNotExecuted reports an operation not sent because validation or
	// earlier execution stopped the batch.
	ErrNotExecuted = errors.New("swd: operation was not executed")
	// ErrIndeterminate reports an operation whose frame might have taken effect.
	ErrIndeterminate = errors.New("swd: operation outcome is indeterminate")
)

type batchResult struct {
	resolved bool
	value    uint32
	err      error
}

func (r *batchResult) resolve(value uint32, err error) {
	if r.resolved {
		return
	}
	r.resolved = true
	r.value = value
	r.err = err
}

// ReadResult reports the outcome of one queued SWD read.
type ReadResult struct {
	result *batchResult
}

// Value returns the value and error from the queued read. Before Commit, it
// returns ErrResultPending.
func (r *ReadResult) Value() (uint32, error) {
	if r == nil || r.result == nil || !r.result.resolved {
		return 0, ErrResultPending
	}
	return r.result.value, r.result.err
}

// WriteResult reports the outcome of one queued SWD write.
type WriteResult struct {
	result *batchResult
}

// Err returns the queued write's completion error. Before Commit, it returns
// ErrResultPending.
func (r *WriteResult) Err() error {
	if r == nil || r.result == nil || !r.result.resolved {
		return ErrResultPending
	}
	return r.result.err
}

type batchOp struct {
	ap     bool
	read   bool
	addr   uint8
	data   uint32
	req    request
	result *batchResult
	err    error
}

// Batch queues an ordered, single-use sequence of raw SWD register operations.
// Calls sharing the batch or its Conn must be serialized. A zero Batch is
// harmless but has no connection, so a nonempty Commit fails before traffic.
type Batch struct {
	conn      *Conn
	ops       []batchOp
	committed bool
}

// NewBatch returns an empty batch. Commit uses the response grammar established
// by Connect.
func (c *Conn) NewBatch() *Batch {
	return &Batch{conn: c}
}

// ReadDP queues one raw debug-port read.
func (b *Batch) ReadDP(addr uint8) *ReadResult {
	return &ReadResult{result: b.queue(batchOp{read: true, addr: addr})}
}

// WriteDP queues one raw debug-port write.
func (b *Batch) WriteDP(addr uint8, value uint32) *WriteResult {
	return &WriteResult{result: b.queue(batchOp{addr: addr, data: value})}
}

// ReadAP queues one raw access-port read.
func (b *Batch) ReadAP(addr uint8) *ReadResult {
	return &ReadResult{result: b.queue(batchOp{ap: true, read: true, addr: addr})}
}

// WriteAP queues one raw access-port write.
func (b *Batch) WriteAP(addr uint8, value uint32) *WriteResult {
	return &WriteResult{result: b.queue(batchOp{ap: true, addr: addr, data: value})}
}

func (b *Batch) queue(op batchOp) *batchResult {
	result := &batchResult{}
	op.result = result
	if b == nil || b.committed {
		result.resolve(0, ErrBatchCommitted)
		return result
	}
	b.ops = append(b.ops, op)
	return result
}

// Commit validates the complete queue before traffic, executes its operations
// in order, and resolves every result. It stops at the first error and does not
// replay a requested register operation.
func (b *Batch) Commit(ctx context.Context) error {
	if b == nil || b.committed {
		return ErrBatchCommitted
	}
	b.committed = true
	if len(b.ops) == 0 {
		return nil
	}
	if err := b.validate(ctx); err != nil {
		b.resolveInvalid()
		return err
	}
	return b.commitSequential(ctx)
}

func (b *Batch) validate(ctx context.Context) error {
	var errs []error
	for i := range b.ops {
		op := &b.ops[i]
		op.req, op.err = newRequest(op.ap, op.read, op.addr)
		if op.err != nil {
			op.err = fmt.Errorf("swd: operation %d: %w", i, op.err)
			errs = append(errs, op.err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if b.conn == nil {
		return errors.New("swd: nil connection")
	}
	if b.conn.state != connectionReady {
		return errors.New("swd: connection is not ready; call Connect")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	shadow := *b.conn
	for i := range b.ops {
		op := &b.ops[i]
		if op.err = shadow.validateTransfer(op.req, op.data); op.err != nil {
			op.err = fmt.Errorf("swd: operation %d: %w", i, op.err)
			errs = append(errs, op.err)
			continue
		}
		shadow.observeTransfer(op.req, 0, op.data, nil)
	}
	return errors.Join(errs...)
}

func (b *Batch) resolveInvalid() {
	for i := range b.ops {
		if b.ops[i].err != nil {
			b.ops[i].result.resolve(0, b.ops[i].err)
		} else {
			b.ops[i].result.resolve(0, ErrNotExecuted)
		}
	}
}

func (b *Batch) resolveSuffix(start int) {
	for i := start; i < len(b.ops); i++ {
		b.ops[i].result.resolve(0, ErrNotExecuted)
	}
}

func (b *Batch) validateRuntimeOperation(index int) error {
	op := &b.ops[index]
	if err := b.conn.validateTransfer(op.req, op.data); err != nil {
		op.err = fmt.Errorf("swd: operation %d: %w", index, err)
		op.result.resolve(0, op.err)
		return op.err
	}
	return nil
}

func (b *Batch) commitSequential(ctx context.Context) error {
	for i := range b.ops {
		if err := ctx.Err(); err != nil {
			b.resolveSuffix(i)
			return err
		}
		if err := b.validateRuntimeOperation(i); err != nil {
			b.resolveSuffix(i + 1)
			return err
		}
		op := &b.ops[i]
		value, err := b.conn.transfer(ctx, op.req, op.data)
		if err != nil && !completeTransfer(err) {
			err = errors.Join(ErrIndeterminate, err)
		}
		op.result.resolve(value, err)
		if err != nil {
			b.resolveSuffix(i + 1)
			return fmt.Errorf("swd: batch stopped at operation %d: %w", i, err)
		}
	}
	return nil
}
