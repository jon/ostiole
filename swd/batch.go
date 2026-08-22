package swd

import (
	"context"
	"errors"
	"fmt"
)

const fixedFrameBits = 54

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

// TransferLimits optionally bounds one SWDIO call. The limit is measured in
// wire bits, includes driven and sampled bits, and must be available without
// traffic.
type TransferLimits interface {
	MaxTransferBits() int
}

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

// Commit validates the complete queue before traffic and resolves every result.
// Simple responses stop at the first error. Overrun responses pack complete
// fixed frames up to the wire's optional TransferLimits value. A wire failure
// makes each operation in that physical chunk indeterminate; later unsent
// operations report ErrNotExecuted.
//
// Commit does not replay a requested register operation. After a packed WAIT,
// it clears STICKYORUN and returns the WAIT. Later frames in the same physical
// chunk which the target abandoned report FAULT. Operations in later chunks
// report ErrNotExecuted. If STICKYORUN cleanup fails, Commit and the WAITed
// result return both errors.
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
	if b.conn.response == responseSimple {
		return b.commitSequential(ctx)
	}
	limit, err := b.conn.transferLimit()
	if err != nil {
		b.resolveSuffix(0)
		return err
	}
	return b.commitOverrun(ctx, limit)
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

func (b *Batch) commitOverrun(ctx context.Context, limit int) error {
	frames := limit / fixedFrameBits
	for start := 0; start < len(b.ops); {
		if err := ctx.Err(); err != nil {
			b.resolveSuffix(start)
			return err
		}
		if err := b.validateRuntimeOperation(start); err != nil {
			b.resolveSuffix(start + 1)
			return err
		}
		end := b.overrunChunkEnd(start, min(start+frames, len(b.ops)))
		if err := b.commitOverrunChunk(ctx, start, end); err != nil {
			b.resolveSuffix(end)
			return err
		}
		start = end
	}
	return nil
}

func (b *Batch) overrunChunkEnd(start, limit int) int {
	shadow := *b.conn
	for i := start; i < limit; i++ {
		op := b.ops[i]
		boundary := shadow.selectPending && isBankZeroCTRLSTATRead(op.req, shadow.bank, shadow.priorBank)
		shadow.observeTransfer(op.req, 0, op.data, nil)
		if boundary {
			return i + 1
		}
	}
	return limit
}

func (b *Batch) commitOverrunChunk(ctx context.Context, start, end int) error {
	seq := &sequence{}
	for i := start; i < end; i++ {
		seq.appendSequence(b.conn.fixedFrame(b.ops[i].req, b.ops[i].data))
	}
	input, err := b.conn.exchange(ctx, seq)
	if err != nil {
		return b.failOverrunExchange(start, end, err)
	}
	values, errs, firstError := b.decodeOverrunChunk(input, start, end)
	waitWithPending := b.observeOverrunChunk(start, end, values, errs)
	return b.finishOverrunChunk(ctx, start, end, errs, firstError, waitWithPending)
}

func (b *Batch) failOverrunExchange(start, end int, err error) error {
	b.conn.requireRepair()
	for i := start; i < end; i++ {
		b.ops[i].result.resolve(0, errors.Join(ErrIndeterminate, err))
	}
	return fmt.Errorf("swd: batch chunk at operation %d: %w", start, err)
}

func (b *Batch) decodeOverrunChunk(input []byte, start, end int) ([]uint32, []error, int) {
	values := make([]uint32, end-start)
	errs := make([]error, end-start)
	firstError := -1
	for i := start; i < end; i++ {
		values[i-start], errs[i-start] = decodeFixedFrame(input, (i-start)*fixedFrameBits, b.ops[i].req)
		if firstError < 0 && errs[i-start] != nil {
			firstError = i
		}
	}
	return values, errs, firstError
}

func (b *Batch) observeOverrunChunk(start, end int, values []uint32, errs []error) bool {
	waitWithPending := false
	for i := start; i < end; i++ {
		op := &b.ops[i]
		resultErr := errs[i-start]
		if resultErr == ErrWait && b.conn.selectPending {
			waitWithPending = true
		} else {
			b.conn.observeTransfer(op.req, values[i-start], op.data, resultErr)
		}
		op.result.resolve(values[i-start], resultErr)
	}
	return waitWithPending
}

func (b *Batch) finishOverrunChunk(ctx context.Context, start, end int, errs []error, firstError int, waitWithPending bool) error {
	if firstError < 0 {
		return nil
	}
	firstErr := errs[firstError-start]
	if firstErr == ErrProtocol {
		b.conn.requireRepair()
		b.markIndeterminate(firstError, end, firstErr)
		return fmt.Errorf("swd: batch stopped at operation %d: %w", firstError, firstErr)
	}
	if firstErr == ErrWait || firstErr == ErrFault {
		if err := b.validateAbandonedSuffix(firstError, end); err != nil {
			return err
		}
	}
	if firstErr == ErrWait {
		if isAbortWrite(b.ops[firstError].req) {
			b.conn.requireRepair()
			cleanupErr := errors.Join(firstErr, errors.New("swd: ABORT returned WAIT; STICKYORUN cleanup is unavailable"))
			b.ops[firstError].result.err = cleanupErr
			return cleanupErr
		}
		if err := b.conn.clearOverrunAfterWAIT(ctx); err != nil {
			cleanupErr := errors.Join(firstErr, err)
			b.ops[firstError].result.err = cleanupErr
			return cleanupErr
		}
		if waitWithPending {
			b.conn.requireRepair()
		}
	}
	return fmt.Errorf("swd: batch stopped at operation %d: %w", firstError, firstErr)
}

func (b *Batch) validateAbandonedSuffix(first, end int) error {
	for i := first + 1; i < end; i++ {
		if b.ops[i].result.err == ErrFault {
			continue
		}
		b.conn.requireRepair()
		b.markIndeterminate(i, end, errors.New("swd: request after WAIT or FAULT was not abandoned"))
		return fmt.Errorf("swd: operation %d completed unexpectedly after operation %d failed", i, first)
	}
	return nil
}

func (b *Batch) markIndeterminate(start, end int, cause error) {
	for i := start; i < end; i++ {
		b.ops[i].result.err = errors.Join(ErrIndeterminate, b.ops[i].result.err, cause)
	}
}

func (c *Conn) transferLimit() (int, error) {
	limits, ok := c.wire.(TransferLimits)
	if !ok {
		return fixedFrameBits, nil
	}
	limit := limits.MaxTransferBits()
	if limit < fixedFrameBits {
		return 0, fmt.Errorf("swd: wire transfer limit %d bits is smaller than one %d-bit frame", limit, fixedFrameBits)
	}
	return limit / fixedFrameBits * fixedFrameBits, nil
}
