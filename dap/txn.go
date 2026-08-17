package dap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/swd"
)

// Result reports the outcome of one queued operation.
type Result struct {
	resolved bool
	value    uint32
	err      error
}

// Value returns the queued operation's result. Before Commit, it returns
// ErrResultPending.
func (r *Result) Value() (uint32, error) {
	if r == nil || !r.resolved {
		return 0, ErrResultPending
	}
	return r.value, r.err
}

func (r *Result) resolve(value uint32, err error) {
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
)

type txnOp struct {
	kind   txnOpKind
	dpReg  DPRegister
	apSel  APSel
	apAddr uint8
	data   uint32
	result *Result
	err    error
}

// Txn queues an ordered, single-use sequence of ADIv5 DP and AP operations.
// Calls sharing the transaction, its DebugPort, or the underlying SWD
// connection must be serialized. Queued operations have the same effects and
// lifecycle requirements as the corresponding DebugPort methods.
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
func (t *Txn) ReadDP(reg DPRegister) *Result {
	return t.queue(txnOp{kind: txnReadDP, dpReg: reg})
}

// WriteDP queues one logical debug-port write. Commit settles the
// write through RDBUFF before its Result reports success. Release does not own
// power-request bits changed this way. Rejection does not invalidate existing
// MemAP values by itself; a completed or indeterminate DAPABORT does.
func (t *Txn) WriteDP(reg DPRegister, value uint32) *Result {
	return t.queue(txnOp{kind: txnWriteDP, dpReg: reg, data: value})
}

// ReadAPIDR queues a read of one access-port identification register. The
// result value is the raw register encoding.
func (t *Txn) ReadAPIDR(sel APSel) *Result {
	return t.queue(txnOp{kind: txnReadAPIDR, apSel: sel, apAddr: apIDRAddress})
}

// ReadRawAP queues one posted access-port read. A read that completes or might
// have completed invalidates existing MemAP values. The caller is responsible
// for effects defined by the selected AP class.
func (t *Txn) ReadRawAP(addr APAddress) *Result {
	return t.queue(txnOp{kind: txnReadRawAP, apSel: addr.sel, apAddr: addr.value})
}

// WriteRawAP queues one access-port write and its completion barrier. A write
// that completes or might have completed invalidates existing MemAP values.
// The caller is responsible for class-specific effects, including writes to
// target memory through a MEM-AP data register.
func (t *Txn) WriteRawAP(addr APAddress, value uint32) *Result {
	return t.queue(txnOp{kind: txnWriteRawAP, apSel: addr.sel, apAddr: addr.value, data: value})
}

func (t *Txn) queue(op txnOp) *Result {
	result := &Result{}
	op.result = result
	if t == nil || t.committed {
		result.resolve(0, ErrTxnCommitted)
		return result
	}
	t.ops = append(t.ops, op)
	return result
}

// Commit validates the complete queue, settles any earlier immediate DP write,
// then executes queued operations in order until one fails. Failure while
// settling the earlier write leaves every queued operation unexecuted. Commit
// resolves every Result before returning. A Txn can be committed only once.
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
		case txnReadRawAP:
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
		if op.kind == txnReadAPIDR || op.kind == txnReadRawAP || op.kind == txnWriteRawAP {
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
	for i := range ops {
		p.lower(i, ops[i])
	}
	return p.steps
}

func (p *txnPlanner) lower(index int, op txnOp) {
	switch op.kind {
	case txnReadDP, txnWriteDP:
		p.lowerDP(index, op)
	case txnReadAPIDR, txnReadRawAP, txnWriteRawAP:
		p.lowerAP(index, op)
	}
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
	read := op.kind == txnReadAPIDR || op.kind == txnReadRawAP
	invalidatesAP := op.kind == txnReadRawAP || op.kind == txnWriteRawAP
	req := apTransferRequest(addr&0x0c, read)
	p.steps = append(p.steps, txnStep{
		req:           req,
		data:          op.data,
		op:            index,
		invalidatesAP: invalidatesAP,
	})
	p.selectPending = false
	p.steps = append(p.steps, txnStep{
		req:              dpTransferRequest(RDBUFF, true),
		dpReg:            RDBUFF,
		op:               index,
		deliver:          true,
		deliverValue:     read,
		operationStarted: true,
		completesWrite:   op.kind == txnWriteRawAP,
		invalidatesAP:    invalidatesAP,
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
	for i := range steps {
		step := &steps[i]
		value, err := t.executeStep(ctx, *step)
		if err != nil {
			return t.failStep(*step, err)
		}
		t.acceptStep(*step, value)
	}
	return nil
}

func (t *Txn) acceptStep(step txnStep, value uint32) {
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

func (t *Txn) failStep(step txnStep, err error) error {
	err = t.executionError(step, err)
	t.applyFailedStepEffect(step, err)
	t.ops[step.op].result.resolve(0, err)
	t.resolveSuffix(step.op + 1)
	return err
}

func (t *Txn) applyFailedStepEffect(step txnStep, err error) {
	if !step.invalidatesAP || t.dp.state.response == responseLost {
		return
	}
	if errors.Is(err, ErrIndeterminate) || step.completesWrite && errors.Is(err, swd.ErrParity) {
		t.dp.state.invalidateAP()
	}
}

func (t *Txn) executeStep(ctx context.Context, step txnStep) (uint32, error) {
	if !step.settlesDPWrite {
		return t.dp.transfer(ctx, step.req, step.data)
	}
	return t.dp.transferDPWriteBarrier(ctx)
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
