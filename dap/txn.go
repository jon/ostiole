package dap

import (
	"context"
	"errors"

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
	txnReadAP
	txnWriteAP
)

type txnOp struct {
	kind   txnOpKind
	dpAddr DPAddress
	apSel  APSel
	apReg  APReg
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

// NewTxn returns an empty transaction for dp.
func (dp *DebugPort) NewTxn() *Txn {
	return &Txn{dp: dp}
}

// ReadDP queues one explicitly banked debug-port read.
func (t *Txn) ReadDP(addr DPAddress) *Result {
	return t.queue(txnOp{kind: txnReadDP, dpAddr: addr})
}

// WriteDP queues one explicitly banked debug-port write. Commit settles the
// write through RDBUFF before its Result reports success. Release does not own
// power-request bits changed this way, and DAPABORT invalidates MemAP values.
func (t *Txn) WriteDP(addr DPAddress, value uint32) *Result {
	return t.queue(txnOp{kind: txnWriteDP, dpAddr: addr, data: value})
}

// ReadAP queues one posted access-port read.
func (t *Txn) ReadAP(sel APSel, reg APReg) *Result {
	return t.queue(txnOp{kind: txnReadAP, apSel: sel, apReg: reg})
}

// WriteAP queues one access-port write and its completion barrier.
func (t *Txn) WriteAP(sel APSel, reg APReg, value uint32) *Result {
	return t.queue(txnOp{kind: txnWriteAP, apSel: sel, apReg: reg, data: value})
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

// Commit validates the complete queue, then executes operations in order until
// one fails. It resolves every Result before returning. A Txn can be committed
// only once.
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
	for i := range t.ops {
		value, err := t.execute(ctx, &t.ops[i])
		if err == nil {
			t.ops[i].result.resolve(value, nil)
			continue
		}
		err = t.executionError(&t.ops[i], err)
		t.ops[i].result.resolve(0, err)
		t.resolveSuffix(i + 1)
		return err
	}
	return nil
}

func (t *Txn) validate() error {
	var errs []error
	for i := range t.ops {
		op := &t.ops[i]
		switch op.kind {
		case txnReadDP:
			op.err = t.dp.validateDPAddress(op.dpAddr, false)
		case txnWriteDP:
			op.err = t.dp.validateDPWrite(op.dpAddr, op.data)
		case txnReadAP, txnWriteAP:
			op.err = validateAPReg(op.apReg)
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
		if op.kind == txnReadAP || op.kind == txnWriteAP {
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

func (t *Txn) execute(ctx context.Context, op *txnOp) (uint32, error) {
	switch op.kind {
	case txnReadDP:
		return t.dp.readDPAt(ctx, op.dpAddr)
	case txnWriteDP:
		if err := t.dp.writeDPAt(ctx, op.dpAddr, op.data); err != nil {
			return 0, err
		}
		return t.dp.readDP(ctx, RDBUFF)
	case txnReadAP:
		return t.dp.readAP(ctx, op.apSel, op.apReg)
	default:
		return 0, t.dp.writeAP(ctx, op.apSel, op.apReg, op.data)
	}
}

func (t *Txn) executionError(op *txnOp, err error) error {
	if t.dp.state.response == responseLost || errors.Is(err, swd.ErrParity) {
		return errors.Join(err, ErrIndeterminate)
	}
	return err
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
