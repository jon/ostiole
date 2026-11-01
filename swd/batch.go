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
	// earlier execution stopped the operation or batch.
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
// it clears STICKYORUN when framing remains usable; otherwise it requires repair.
// Later non-exempt frames which the target abandoned in that chunk report FAULT. Operations in later chunks
// report ErrNotExecuted. If STICKYORUN cleanup fails, Commit and the WAITed
// result return both errors. Cancellation stops the next frame or chunk;
// completed results retain their physical outcomes while Commit reports the
// context error. Started frames use the independent completion budget.
//
// Every response is inspected, even after an earlier parity error. Valid DPIDR,
// bank-zero CTRL/STAT, and ABORT results remain confirmed after WAIT or FAULT.
// A chunk ends after ABORT, which can clear sticky state. Protocol loss marks
// its remaining suffix indeterminate and requires repair without more traffic.
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
	if err := b.conn.validateCompletion(); err != nil {
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
		value, err := b.conn.transfer(context.WithoutCancel(ctx), op.req, op.data)
		if err != nil && !completeTransfer(err) {
			err = errors.Join(ErrIndeterminate, err)
		}
		op.result.resolve(value, err)
		if err != nil {
			b.resolveSuffix(i + 1)
			return completionError(ctx, fmt.Errorf("swd: batch stopped at operation %d: %w", i, err))
		}
		if err := ctx.Err(); err != nil {
			b.resolveSuffix(i + 1)
			return err
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
		boundary := shadow.selectPending && isPossibleCTRLSTATRead(op.req, shadow.priorBank)
		shadow.observeTransfer(op.req, 0, op.data, nil)
		if boundary || isAbortWrite(op.req) {
			return i + 1
		}
	}
	return limit
}

func (b *Batch) commitOverrunChunk(ctx context.Context, start, end int) error {
	completionCtx, cancel, err := b.conn.completionContext(ctx)
	if err != nil {
		b.resolveSuffix(start)
		return err
	}
	defer cancel()
	seq := &sequence{}
	for i := start; i < end; i++ {
		seq.appendSequence(b.conn.fixedFrame(b.ops[i].req, b.ops[i].data))
	}
	input, err := b.conn.exchange(completionCtx, seq)
	if err != nil {
		return completionError(ctx, b.failOverrunExchange(start, end, err))
	}
	values, errs := b.decodeOverrunChunk(input, start, end)
	waitWithPending, exempt := b.observeOverrunChunk(start, end, values, errs)
	return completionError(ctx, b.finishOverrunChunk(completionCtx, start, end, errs, waitWithPending, exempt))
}

func (b *Batch) failOverrunExchange(start, end int, err error) error {
	b.conn.requireRepair()
	for i := start; i < end; i++ {
		b.ops[i].result.resolve(0, errors.Join(ErrIndeterminate, err))
	}
	return fmt.Errorf("swd: batch chunk at operation %d: %w", start, err)
}

func (b *Batch) decodeOverrunChunk(input []byte, start, end int) ([]uint32, []error) {
	values := make([]uint32, end-start)
	errs := make([]error, end-start)
	for i := start; i < end; i++ {
		values[i-start], errs[i-start] = decodeFixedFrame(input, (i-start)*fixedFrameBits, b.ops[i].req)
	}
	return values, errs
}

func (b *Batch) observeOverrunChunk(start, end int, values []uint32, errs []error) (bool, []bool) {
	waitWithPending := false
	exempt := make([]bool, end-start)
	for i := start; i < end; i++ {
		op := &b.ops[i]
		resultErr := errs[i-start]
		exempt[i-start] = b.conn.stickyExempt(op.req)
		if resultErr == ErrWait && b.conn.selectPending {
			waitWithPending = true
		} else {
			b.conn.observeTransfer(op.req, values[i-start], op.data, resultErr)
		}
		op.result.resolve(values[i-start], resultErr)
	}
	return waitWithPending, exempt
}

func (c *Conn) stickyExempt(req request) bool {
	if isDPIDRRead(req) || isAbortWrite(req) {
		return true
	}
	bankZero := c.bank.valid && c.bank.bank == 0
	if c.selectPending {
		bankZero = bankZero && c.priorBank.valid && c.priorBank.bank == 0
	}
	return !req.isAP() && req.isRead() && req.address() == 0x04 && bankZero
}

func firstFrameError(errs []error, matches ...error) int {
	for index, err := range errs {
		for _, match := range matches {
			if err == match {
				return index
			}
		}
	}
	return -1
}

func (b *Batch) finishOverrunChunk(ctx context.Context, start, end int, errs []error, waitWithPending bool, exempt []bool) error {
	abandonedEnd := end
	if protocol := firstFrameError(errs, ErrProtocol); protocol >= 0 {
		abandonedEnd = start + protocol
		b.conn.requireRepair()
		b.markIndeterminate(abandonedEnd, end, ErrProtocol)
	}
	var suffixErr error
	if failed := firstFrameError(errs, ErrWait, ErrFault); failed >= 0 {
		suffixErr = b.validateAbandonedSuffix(start+failed, abandonedEnd, start, exempt)
	}
	if waited := firstFrameError(errs, ErrWait); waited >= 0 && b.conn.state == connectionReady {
		b.cleanupPackedWAIT(ctx, start+waited, waitWithPending)
	}
	var results []error
	for i := start; i < end; i++ {
		if err := b.ops[i].result.err; err != nil {
			results = append(results, fmt.Errorf("swd: operation %d: %w", i, err))
		}
	}
	if err := errors.Join(append(results, suffixErr)...); err != nil {
		return fmt.Errorf("swd: batch chunk at operation %d: %w", start, err)
	}
	return nil
}

func (b *Batch) cleanupPackedWAIT(ctx context.Context, index int, waitWithPending bool) {
	var err error
	if isAbortWrite(b.ops[index].req) {
		b.conn.requireRepair()
		err = errors.New("swd: ABORT returned WAIT; STICKYORUN cleanup is unavailable")
	} else {
		err = b.conn.clearOverrunAfterWAIT(ctx)
	}
	if err != nil {
		b.ops[index].result.err = errors.Join(b.ops[index].result.err, err)
	}
	if waitWithPending {
		b.conn.requireRepair()
	}
}

func (b *Batch) validateAbandonedSuffix(first, end, start int, exempt []bool) error {
	for i := first + 1; i < end; i++ {
		err := b.ops[i].result.err
		if err == ErrFault || exempt[i-start] && (err == nil || err == ErrParity) {
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
