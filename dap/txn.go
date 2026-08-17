package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

type txnResult struct {
	resolved bool
	value    uint32
	err      error
}

// ReadResult reports the outcome of one queued read.
type ReadResult struct {
	result *txnResult
}

// Value returns the value and error from the queued read. Before Commit, it
// returns ErrResultPending.
func (r *ReadResult) Value() (uint32, error) {
	if r == nil || r.result == nil || !r.result.resolved {
		return 0, ErrResultPending
	}
	return r.result.value, r.result.err
}

// WriteResult reports the outcome of one queued write.
type WriteResult struct {
	result *txnResult
}

// Err returns the queued write's completion error. Before Commit, it returns
// ErrResultPending.
func (r *WriteResult) Err() error {
	if r == nil || r.result == nil || !r.result.resolved {
		return ErrResultPending
	}
	return r.result.err
}

func (r *txnResult) resolve(value uint32, err error) {
	if r.resolved {
		return
	}
	r.resolved = true
	r.value = value
	r.err = err
}

type txnOpKind uint8

const (
	txnReadDP txnOpKind = iota
	txnWriteDP
	txnReadAPIDR
	txnReadRawAP
	txnWriteRawAP
	txnReadAPSequential
)

type txnOp struct {
	kind       txnOpKind
	dpReg      DPRegister
	apSel      APSel
	apAddr     uint8
	data       uint32
	preserveAP bool
	result     *txnResult
	err        error
}

// Txn queues an ordered, single-use sequence of ADIv5 DP and AP operations.
// Calls sharing the transaction, its DebugPort, or the underlying SWD
// connection must be serialized. Queued operations have the same effects and
// lifecycle requirements as the corresponding DebugPort methods. Commit can
// pack their physical SWD requests while preserving logical result order.
type Txn struct {
	dp        *DebugPort
	ops       []txnOp
	committed bool
}

// NewTxn returns an empty transaction for dp. Commit requires an active
// connection.
func (dp *DebugPort) NewTxn() *Txn {
	return &Txn{dp: dp}
}

// ReadDP queues one logical debug-port read.
func (t *Txn) ReadDP(reg DPRegister) *ReadResult {
	return &ReadResult{result: t.queue(txnOp{kind: txnReadDP, dpReg: reg})}
}

// WriteDP queues one logical debug-port write. Commit settles the write through
// RDBUFF before its WriteResult reports success. CTRL/STAT writes must preserve
// the connection-owned ORUNDETECT bit. Release does not restore power-request
// bits changed this way. Rejection does not invalidate existing MemAP values by
// itself; a completed or indeterminate DAPABORT does.
func (t *Txn) WriteDP(reg DPRegister, value uint32) *WriteResult {
	return &WriteResult{result: t.queue(txnOp{kind: txnWriteDP, dpReg: reg, data: value})}
}

// ReadAPIDR queues a read of one access-port identification register. The
// result value is the raw register encoding.
func (t *Txn) ReadAPIDR(sel APSel) *ReadResult {
	return &ReadResult{result: t.queue(txnOp{kind: txnReadAPIDR, apSel: sel, apAddr: apIDRAddress})}
}

// ReadRawAP queues one posted access-port read. A read that completes or might
// have completed invalidates existing MemAP values. The caller is responsible
// for effects defined by the selected AP class.
func (t *Txn) ReadRawAP(addr APAddress) *ReadResult {
	return &ReadResult{result: t.queue(txnOp{kind: txnReadRawAP, apSel: addr.sel, apAddr: addr.value})}
}

// WriteRawAP queues one access-port write and its completion barrier. A write
// that completes or might have completed invalidates existing MemAP values.
// The caller is responsible for class-specific effects, including writes to
// target memory through a MEM-AP data register.
func (t *Txn) WriteRawAP(addr APAddress, value uint32) *WriteResult {
	return &WriteResult{result: t.queue(txnOp{kind: txnWriteRawAP, apSel: addr.sel, apAddr: addr.value, data: value})}
}

func (t *Txn) readAP(sel APSel, addr uint8) *ReadResult {
	return &ReadResult{result: t.queue(txnOp{kind: txnReadRawAP, apSel: sel, apAddr: addr, preserveAP: true})}
}

func (t *Txn) writeAP(sel APSel, addr uint8, value uint32) *WriteResult {
	return &WriteResult{result: t.queue(txnOp{kind: txnWriteRawAP, apSel: sel, apAddr: addr, data: value, preserveAP: true})}
}

func (t *Txn) readAPSequential(sel APSel, addr uint8) *ReadResult {
	return &ReadResult{result: t.queue(txnOp{kind: txnReadAPSequential, apSel: sel, apAddr: addr, preserveAP: true})}
}

func (t *Txn) queue(op txnOp) *txnResult {
	result := &txnResult{}
	op.result = result
	if t == nil || t.committed {
		result.resolve(0, ErrTxnCommitted)
		return result
	}
	t.ops = append(t.ops, op)
	return result
}

// Commit validates the complete queue, settles any earlier immediate DP write,
// then executes queued operations in order until one fails. The underlying SWD
// connection can pack fixed frames without changing logical result order.
// Failure while settling the earlier write leaves every queued operation
// unexecuted. Commit resolves every result before returning. A Txn can be
// committed only once.
func (t *Txn) Commit(ctx context.Context) error {
	if t == nil || t.committed {
		return ErrTxnCommitted
	}
	t.committed = true
	if len(t.ops) == 0 {
		return nil
	}
	if t.dp == nil {
		err := errors.New("dap: nil debug port")
		t.resolveAll(err)
		return err
	}
	if err := t.validate(); err != nil {
		t.resolveInvalid()
		return err
	}
	if err := t.checkAccess(); err != nil {
		t.resolveInvalid()
		return err
	}
	if err := t.dp.settlePreviousDPWrite(ctx); err != nil {
		t.resolveSuffix(0)
		return err
	}
	return t.execute(ctx, newTxnPlanner(t.dp).plan(t.ops))
}

func (dp *DebugPort) settlePreviousDPWrite(ctx context.Context) error {
	if !dp.state.dpWritePending {
		return nil
	}
	_, err := dp.transferDPWriteBarrier(ctx)
	if err != nil {
		return fmt.Errorf("dap: settle previous DP write before transaction: %w", err)
	}
	return nil
}

func (t *Txn) validate() error {
	var errs []error
	for i := range t.ops {
		op := &t.ops[i]
		switch op.kind {
		case txnReadDP:
			_, op.err = t.dp.validateDPRegister(op.dpReg, false)
		case txnWriteDP:
			_, op.err = t.dp.validateDPWrite(op.dpReg, op.data)
		case txnReadAPIDR:
			_, op.err = validateAPSel(op.apSel)
		case txnReadRawAP, txnReadAPSequential:
			_, op.err = validateAPAddress(APAddress{sel: op.apSel, value: op.apAddr}, false)
		case txnWriteRawAP:
			_, op.err = validateAPAddress(APAddress{sel: op.apSel, value: op.apAddr}, true)
		}
		if op.err != nil {
			errs = append(errs, op.err)
		}
	}
	return errors.Join(errs...)
}

func (t *Txn) checkAccess() error {
	for i := range t.ops {
		op := &t.ops[i]
		if op.kind == txnReadAPIDR || op.kind == txnReadRawAP || op.kind == txnWriteRawAP || op.kind == txnReadAPSequential {
			op.err = t.dp.requireConnected()
		} else {
			op.err = t.dp.requireOperational()
		}
		if op.err != nil {
			return op.err
		}
	}
	return nil
}

func (t *Txn) executionError(step txnStep, err error) error {
	writeBarrier := step.settlesDPWrite || step.completesWrite
	if writeBarrier && errors.Is(err, swd.ErrParity) {
		return err
	}
	if step.operationStarted && (!writeBarrier || !faultReportsWriteDataError(err)) {
		return errors.Join(err, ErrIndeterminate)
	}
	if requestWasRejected(err) {
		return err
	}
	if errors.Is(err, swd.ErrParity) || t.dp.state.response == responseLost && !errors.Is(err, swd.ErrFault) {
		return errors.Join(err, ErrIndeterminate)
	}
	return err
}

func faultReportsWriteDataError(err error) bool {
	var fault *FaultError
	return errors.As(err, &fault) && fault.StateValid && fault.CTRLSTAT&writeDataError != 0
}

func faultHasValidState(err error) bool {
	var fault *FaultError
	return errors.As(err, &fault) && fault.StateValid
}

type txnStep struct {
	req              transferRequest
	dpReg            DPRegister
	data             uint32
	op               int
	deliver          bool
	deliverValue     bool
	operationStarted bool
	settlesDPWrite   bool
	completesWrite   bool
	apRead           bool
	apWrite          bool
	invalidatesAP    bool
}

type txnPlanner struct {
	selectDP      selectState
	selectPending bool
	steps         []txnStep
}

func newTxnPlanner(dp *DebugPort) *txnPlanner {
	return &txnPlanner{selectDP: dp.state.selectDP, selectPending: dp.state.selectPending}
}

func (p *txnPlanner) plan(ops []txnOp) []txnStep {
	for i := 0; i < len(ops); {
		if ops[i].kind != txnReadAPSequential {
			p.lower(i, ops[i])
			i++
			continue
		}
		end := i + 1
		for end < len(ops) && sameSequentialAP(ops[i], ops[end]) {
			end++
		}
		p.lowerSequentialAP(i, ops[i:end])
		i = end
	}
	return p.steps
}

func sameSequentialAP(first, next txnOp) bool {
	return next.kind == txnReadAPSequential && first.apSel == next.apSel && first.apAddr == next.apAddr
}

func (p *txnPlanner) lower(index int, op txnOp) {
	switch op.kind {
	case txnReadDP, txnWriteDP:
		p.lowerDP(index, op)
	case txnReadAPIDR, txnReadRawAP, txnWriteRawAP:
		p.lowerAP(index, op)
	}
}

func (p *txnPlanner) lowerSequentialAP(start int, ops []txnOp) {
	selection, _ := ops[0].apSel.Value()
	value := uint32(selection)<<24 | uint32(ops[0].apAddr&0xf0)
	p.selectValue(start, value)
	p.settleSELECT(start)
	for i := range ops {
		step := txnStep{req: apTransferRequest(ops[i].apAddr&0x0c, true), op: start + i, apRead: true}
		if i > 0 {
			step.op--
			step.deliver = true
			step.deliverValue = true
			step.operationStarted = true
		}
		p.steps = append(p.steps, step)
	}
	p.steps = append(p.steps, txnStep{req: dpTransferRequest(RDBUFF, true), dpReg: RDBUFF, op: start + len(ops) - 1, deliver: true, deliverValue: true, operationStarted: true, apRead: true})
}

func (p *txnPlanner) lowerDP(index int, op txnOp) {
	info, _ := describeDPRegister(op.dpReg)
	if !info.bankIndependent {
		p.selectBank(index, info.bank)
	}
	invalidatesAP := op.kind == txnWriteDP && op.dpReg == ABORT && op.data&dapAbort != 0
	step := txnStep{
		req:           txnDPRequest(op, info),
		dpReg:         op.dpReg,
		data:          op.data,
		op:            index,
		deliver:       op.kind == txnReadDP,
		deliverValue:  op.kind == txnReadDP,
		invalidatesAP: invalidatesAP,
	}
	p.steps = append(p.steps, step)
	if op.kind == txnWriteDP && op.dpReg == SELECT {
		p.selectDP = selectState{value: op.data, valid: true}
		p.selectPending = true
	}
	if op.kind == txnWriteDP && op.dpReg == ABORT && p.selectPending {
		p.selectDP = selectState{}
		p.selectPending = false
	}
	if p.selectPending && settlesSELECT(step.req) {
		p.selectPending = false
	}
	if op.kind == txnWriteDP {
		p.steps = append(p.steps, txnStep{
			req:              dpTransferRequest(RDBUFF, true),
			dpReg:            RDBUFF,
			op:               index,
			deliver:          true,
			operationStarted: true,
			settlesDPWrite:   true,
			completesWrite:   true,
			invalidatesAP:    invalidatesAP,
		})
		p.selectPending = false
	}
}

func txnDPRequest(op txnOp, info dpRegisterInfo) transferRequest {
	return transferRequest{Read: op.kind == txnReadDP, Addr: info.offset}
}

func (p *txnPlanner) lowerAP(index int, op txnOp) {
	addr := op.apAddr
	selection, _ := op.apSel.Value()
	value := uint32(selection)<<24 | uint32(addr&0xf0)
	p.selectValue(index, value)
	p.settleSELECT(index)
	read := op.kind == txnReadAPIDR || op.kind == txnReadRawAP
	invalidatesAP := op.kind == txnReadRawAP || op.kind == txnWriteRawAP
	req := apTransferRequest(addr&0x0c, read)
	p.steps = append(p.steps, txnStep{
		apRead:        op.kind == txnReadRawAP,
		apWrite:       op.kind == txnWriteRawAP,
		req:           req,
		data:          op.data,
		op:            index,
		invalidatesAP: invalidatesAP && !op.preserveAP,
	})
	p.steps = append(p.steps, txnStep{
		req:              dpTransferRequest(RDBUFF, true),
		dpReg:            RDBUFF,
		op:               index,
		deliver:          true,
		deliverValue:     read,
		operationStarted: true,
		apRead:           op.kind == txnReadRawAP,
		apWrite:          op.kind == txnWriteRawAP,
		completesWrite:   op.kind == txnWriteRawAP,
		invalidatesAP:    invalidatesAP && !op.preserveAP,
	})
}

func (p *txnPlanner) selectBank(index int, bank uint8) {
	value := uint32(bank)
	if p.selectDP.valid {
		value |= p.selectDP.value &^ 0x0f
	}
	p.selectValue(index, value)
	p.settleSELECT(index)
}

func (p *txnPlanner) selectValue(index int, value uint32) {
	if p.selectDP.valid && p.selectDP.value == value {
		return
	}
	p.steps = append(p.steps, txnStep{
		req:   dpTransferRequest(SELECT, false),
		dpReg: SELECT,
		data:  value,
		op:    index,
	})
	p.selectDP = selectState{value: value, valid: true}
	p.selectPending = true
}

func (p *txnPlanner) settleSELECT(index int) {
	if !p.selectPending {
		return
	}
	p.steps = append(p.steps, txnStep{req: dpTransferRequest(RDBUFF, true), dpReg: RDBUFF, op: index, settlesDPWrite: true})
	p.selectPending = false
}

func settlesSELECT(req transferRequest) bool {
	if req.AP || req.Addr == dpRegisterOffset(RDBUFF) {
		return true
	}
	if req.Read {
		return req.Addr != dpRegisterOffset(DPIDR)
	}
	return req.Addr != dpRegisterOffset(ABORT) && req.Addr != dpRegisterOffset(SELECT)
}

func (t *Txn) execute(ctx context.Context, steps []txnStep) error {
	waits := make(map[txnStep]int)
	for cursor := 0; cursor < len(steps); {
		end := nextBatchBoundary(steps, cursor)
		results, batchErr := t.transferSteps(ctx, steps[cursor:end])
		failed := firstFailedTransfer(results)
		t.acceptBatchPrefix(steps[cursor:end], results, failed)
		cursor += failed
		if failed == len(results) {
			cursor = end
			continue
		}
		if err := t.handleBatchFailure(ctx, steps[cursor:end], results[failed:], batchErr, waits); err != nil {
			return err
		}
	}
	return nil
}

func nextBatchBoundary(steps []txnStep, start int) int {
	if txnResponseBoundary(steps[start]) {
		return start + 1
	}
	end := start + 1
	for end < len(steps) && !txnResponseBoundary(steps[end]) {
		end++
	}
	return end
}

func txnResponseBoundary(step txnStep) bool {
	return step.dpReg == DPIDR || step.dpReg == CTRLSTAT || step.dpReg == ABORT
}

type txnTransferResult struct {
	read  *swd.ReadResult
	write *swd.WriteResult
}

func (r txnTransferResult) value() (uint32, error) {
	if r.read != nil {
		return r.read.Value()
	}
	return 0, r.write.Err()
}

func (r txnTransferResult) err() error {
	_, err := r.value()
	return err
}

func (t *Txn) transferSteps(ctx context.Context, steps []txnStep) ([]txnTransferResult, error) {
	batch := t.dp.conn.NewBatch()
	results := make([]txnTransferResult, len(steps))
	for i := range steps {
		step := steps[i]
		switch {
		case step.req.AP && step.req.Read:
			results[i].read = batch.ReadAP(step.req.Addr)
		case step.req.AP:
			results[i].write = batch.WriteAP(step.req.Addr, step.data)
		case step.req.Read:
			results[i].read = batch.ReadDP(step.req.Addr)
		default:
			results[i].write = batch.WriteDP(step.req.Addr, step.data)
		}
	}
	return results, batch.Commit(ctx)
}

func firstFailedTransfer(results []txnTransferResult) int {
	for i := range results {
		if results[i].err() != nil {
			return i
		}
	}
	return len(results)
}

func (t *Txn) acceptBatchPrefix(steps []txnStep, results []txnTransferResult, count int) {
	for i := range count {
		value, _ := results[i].value()
		t.acceptStep(steps[i], value)
	}
}

func (t *Txn) handleBatchFailure(ctx context.Context, steps []txnStep, results []txnTransferResult, batchErr error, waits map[txnStep]int) error {
	err := results[0].err()
	if errors.Is(err, swd.ErrIndeterminate) {
		return t.failBatchTransport(steps, results, batchErr)
	}
	if errors.Is(err, swd.ErrNotExecuted) {
		return t.failStep(steps[0], batchErr)
	}
	value, _ := results[0].value()
	if errors.Is(err, swd.ErrWait) {
		if err != swd.ErrWait {
			return t.failBatchWAITCleanup(steps, results, err)
		}
		return t.retryBatchWAIT(ctx, steps, results, waits)
	}
	t.observeStep(steps[0], value, err)
	if errors.Is(err, swd.ErrFault) {
		if !batchSuffixAbandoned(results) {
			return t.failUnexpectedBatchSuffix(steps[:clockedTransferCount(results)], err)
		}
		return t.failStep(steps[0], t.dp.handleFault(steps[0].req, stepMayAffectAP(steps[0])))
	}
	if errors.Is(err, swd.ErrParity) {
		clocked := clockedTransferCount(results)
		if clocked == 1 {
			return t.failStep(steps[0], err)
		}
		if steps[0].settlesDPWrite || steps[0].completesWrite {
			return t.failCompletedBatchBarrier(steps[:clocked], err)
		}
		return t.failClockedSuffix(steps[:clocked], err)
	}
	return t.failClockedSuffix(steps[:clockedTransferCount(results)], errors.Join(err, batchErr))
}

func (t *Txn) failBatchWAITCleanup(steps []txnStep, results []txnTransferResult, err error) error {
	if !batchSuffixAbandoned(results) {
		return t.failUnexpectedBatchSuffix(steps[:clockedTransferCount(results)], err)
	}
	step := steps[0]
	err = t.executionError(step, err)
	t.dp.state.loseFraming()
	t.ops[step.op].result.resolve(0, err)
	t.resolveSuffix(step.op + 1)
	return err
}

func (t *Txn) retryBatchWAIT(ctx context.Context, steps []txnStep, results []txnTransferResult, waits map[txnStep]int) error {
	if !batchSuffixAbandoned(results) {
		return t.failUnexpectedBatchSuffix(steps[:clockedTransferCount(results)], swd.ErrWait)
	}
	step := steps[0]
	if t.dp.state.selectPending {
		cause := errors.Join(swd.ErrWait, ErrIndeterminate, errors.New("dap: WAIT cleanup abandoned a pending SELECT write"))
		t.dp.state.loseFraming()
		return t.failStep(step, cause)
	}
	t.observeStep(step, 0, swd.ErrWait)
	if err := t.dp.validateWait(step.req, swd.ErrWait); err != nil {
		return t.failStep(step, err)
	}
	waits[step]++
	if err := ctx.Err(); err != nil {
		cause := errors.Join(swd.ErrWait, err)
		return t.failStep(step, t.dp.finishWait(cause, stepMayAffectAP(step)))
	}
	if waits[step] <= maxWaitRetries {
		return nil
	}
	cause := fmt.Errorf("dap: WAIT retry limit exceeded: %w", swd.ErrWait)
	return t.failStep(step, t.dp.finishWait(cause, stepMayAffectAP(step)))
}

func batchSuffixAbandoned(results []txnTransferResult) bool {
	unsent := false
	for i := 1; i < len(results); i++ {
		err := results[i].err()
		if errors.Is(err, swd.ErrNotExecuted) {
			unsent = true
			continue
		}
		if unsent || !errors.Is(err, swd.ErrFault) {
			return false
		}
	}
	return true
}

func clockedTransferCount(results []txnTransferResult) int {
	for i := 1; i < len(results); i++ {
		if errors.Is(results[i].err(), swd.ErrNotExecuted) {
			return i
		}
	}
	return len(results)
}

func (t *Txn) failUnexpectedBatchSuffix(steps []txnStep, ackErr error) error {
	cause := errors.Join(ackErr, ErrIndeterminate, errors.New("dap: a request after WAIT or FAULT was not abandoned"))
	t.dp.state.loseFraming()
	t.resolveIndeterminateSteps(steps, cause)
	t.resolveSuffix(steps[len(steps)-1].op + 1)
	return cause
}

func (t *Txn) failBatchTransport(steps []txnStep, results []txnTransferResult, batchErr error) error {
	primary := errors.Join(batchErr, ErrIndeterminate)
	t.dp.state.loseFraming()
	lastOp := steps[0].op
	for i := range steps {
		if i >= len(results) || !errors.Is(results[i].err(), swd.ErrIndeterminate) {
			break
		}
		err := error(ErrIndeterminate)
		if i == 0 {
			err = primary
		}
		t.ops[steps[i].op].result.resolve(0, err)
		lastOp = steps[i].op
	}
	t.resolveSuffix(lastOp + 1)
	return primary
}

func (t *Txn) failClockedSuffix(steps []txnStep, err error) error {
	primary := errors.Join(err, ErrIndeterminate)
	t.dp.state.loseFraming()
	t.resolveIndeterminateSteps(steps, primary)
	t.resolveSuffix(steps[len(steps)-1].op + 1)
	return primary
}

func (t *Txn) failCompletedBatchBarrier(steps []txnStep, err error) error {
	step := steps[0]
	err = t.executionError(step, err)
	t.applyFailedStepEffect(step, err)
	if len(steps) == 1 {
		t.ops[step.op].result.resolve(0, err)
		t.resolveSuffix(step.op + 1)
		return err
	}
	primary := errors.Join(err, ErrIndeterminate)
	t.dp.state.loseFraming()
	if steps[1].op == step.op {
		t.ops[step.op].result.resolve(0, primary)
	} else {
		t.ops[step.op].result.resolve(0, err)
	}
	t.resolveIndeterminateSteps(steps[1:], primary)
	t.resolveSuffix(steps[len(steps)-1].op + 1)
	return primary
}

func (t *Txn) resolveIndeterminateSteps(steps []txnStep, err error) {
	for i := range steps {
		t.ops[steps[i].op].result.resolve(0, err)
	}
}

func (t *Txn) acceptStep(step txnStep, value uint32) {
	t.observeStep(step, value, nil)
	if !step.req.Read && !step.req.AP {
		if step.invalidatesAP {
			t.dp.recordDPWriteState(step.dpReg, step.data)
		} else {
			t.dp.recordDPWrite(step.dpReg, step.data)
		}
	}
	if step.req.Read && !step.req.AP {
		t.dp.recordDPRead(step.dpReg, value)
	}
	if step.deliver && step.invalidatesAP {
		t.dp.state.invalidateAP()
	}
	if !step.deliver {
		return
	}
	if !step.deliverValue {
		value = 0
	}
	t.ops[step.op].result.resolve(value, nil)
}

func (t *Txn) observeStep(step txnStep, value uint32, err error) {
	t.dp.resolveSELECT(step.req, value, err)
	if step.settlesDPWrite && (err == nil || errors.Is(err, swd.ErrParity) || faultHasValidState(err)) {
		t.dp.state.settleDPWrite()
	} else if !step.settlesDPWrite && responseSettlesPreviousDPWrite(step.req, err) {
		t.dp.state.settleDPWrite()
	}
}

func stepMayAffectAP(step txnStep) bool {
	return step.req.AP || step.operationStarted && !step.settlesDPWrite
}

func (t *Txn) failStep(step txnStep, err error) error {
	err = t.executionError(step, err)
	t.applyFailedStepEffect(step, err)
	t.ops[step.op].result.resolve(0, err)
	t.resolveSuffix(step.op + 1)
	return err
}

func (t *Txn) applyFailedStepEffect(step txnStep, err error) {
	if (!step.invalidatesAP && !step.apRead && !step.apWrite) || t.dp.state.response == responseLost {
		return
	}
	if errors.Is(err, ErrIndeterminate) || step.completesWrite && errors.Is(err, swd.ErrParity) {
		t.dp.state.invalidateAP()
	}
}

func (t *Txn) resolveInvalid() {
	for i := range t.ops {
		err := t.ops[i].err
		if err == nil {
			err = ErrNotExecuted
		}
		t.ops[i].result.resolve(0, err)
	}
}

func (t *Txn) resolveAll(err error) {
	for i := range t.ops {
		t.ops[i].result.resolve(0, err)
	}
}

func (t *Txn) resolveSuffix(start int) {
	for i := start; i < len(t.ops); i++ {
		t.ops[i].result.resolve(0, ErrNotExecuted)
	}
}
