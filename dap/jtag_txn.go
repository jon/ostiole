package dap

import (
	"context"
	"errors"
)

func (e *jtagExecutor) executeTxn(ctx context.Context, txn *Txn) error {
	for i := range txn.ops {
		op := &txn.ops[i]
		value, err := e.executeOp(ctx, op)
		op.result.resolve(value, err)
		if err != nil {
			txn.resolveSuffix(i + 1)
			return err
		}
	}
	return nil
}

func (e *jtagExecutor) executeOp(ctx context.Context, op *txnOp) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	switch op.kind {
	case txnReadDP:
		return e.dp.readDP(ctx, op.dpReg)
	case txnWriteDP:
		return 0, e.dp.writeDP(ctx, op.dpReg, op.data)
	case txnWriteAPSequence:
		return 0, e.writeSequence(ctx, op)
	default:
		return e.executeAP(ctx, op)
	}
}

func (e *jtagExecutor) executeAP(ctx context.Context, op *txnOp) (uint32, error) {
	generation := e.dp.state.apGeneration
	var value uint32
	var possible bool
	var err error
	if op.kind == txnWriteRawAP {
		possible, err = e.dp.writeAPEffect(ctx, op.apSel, op.apAddr, op.data)
	} else {
		possible, value, err = e.dp.readAPEffect(ctx, op.apSel, op.apAddr)
	}
	raw := op.kind == txnWriteRawAP || op.kind == txnReadRawAP
	if possible && raw && (!op.preserveAP || errors.Is(err, ErrIndeterminate)) && e.dp.state.apGeneration == generation {
		e.dp.state.invalidateAP()
	}
	return value, err
}

func (e *jtagExecutor) writeSequence(ctx context.Context, op *txnOp) error {
	for _, value := range op.values {
		possible, err := e.dp.writeAPEffect(ctx, op.apSel, op.apAddr, value)
		if possible {
			op.accepted++
		}
		if err != nil {
			op.uncertainWrite = possible
			return err
		}
		op.confirmed++
	}
	return nil
}
