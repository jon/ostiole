package dap

import (
	"context"
	"errors"
)

func (dp *DebugPort) executeSequentialTxn(ctx context.Context, txn *Txn) error {
	if dp.conn != nil {
		if err := dp.settlePreviousDPWrite(ctx); err != nil {
			txn.resolveSuffix(0)
			return err
		}
	}
	for i := range txn.ops {
		op := &txn.ops[i]
		value, err := dp.executeOp(ctx, op)
		op.result.resolve(value, err)
		if err != nil {
			txn.resolveSuffix(i + 1)
			return err
		}
	}
	return nil
}

func (dp *DebugPort) executeOp(ctx context.Context, op *txnOp) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if dp.conn != nil && op.kind != txnWriteAPSequence {
		return dp.executeSWDOp(ctx, op)
	}
	switch op.kind {
	case txnReadDP:
		return dp.readDP(ctx, op.dpReg)
	case txnWriteDP:
		err := dp.writeDP(ctx, op.dpReg, op.data)
		if err == nil && dp.conn != nil {
			err = dp.settlePreviousDPWrite(ctx)
		}
		return 0, err
	case txnWriteAPSequence:
		return 0, dp.writeSequence(ctx, op)
	default:
		return dp.executeAP(ctx, op)
	}
}

func (dp *DebugPort) executeAP(ctx context.Context, op *txnOp) (uint32, error) {
	generation := dp.state.apGeneration
	var value uint32
	var possible bool
	var err error
	if op.kind == txnWriteRawAP {
		possible, err = dp.writeAPEffect(ctx, op.apSel, op.apAddr, op.data)
	} else {
		possible, value, err = dp.readAPEffect(ctx, op.apSel, op.apAddr)
	}
	raw := op.kind == txnWriteRawAP || op.kind == txnReadRawAP
	if possible && raw && (!op.preserveAP || errors.Is(err, ErrIndeterminate)) && dp.state.apGeneration == generation {
		dp.state.invalidateAP()
	}
	return value, err
}

func (dp *DebugPort) writeSequence(ctx context.Context, op *txnOp) error {
	if dp.conn != nil {
		return dp.writeSWDSequence(ctx, op)
	}
	for _, value := range op.values {
		possible, err := dp.writeAPEffect(ctx, op.apSel, op.apAddr, value)
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

func (dp *DebugPort) executeSWDOp(ctx context.Context, op *txnOp) (uint32, error) {
	txn := &Txn{dp: dp, ops: []txnOp{*op}}
	if op.kind == txnReadDP || op.kind == txnWriteDP {
		err := dp.conn.executeTxn(ctx, txn)
		return op.result.value, err
	}
	if err := dp.selectAP(ctx, op.apSel, op.apAddr); err != nil {
		return 0, err
	}
	planner := newSWDTxnPlanner(dp)
	if op.kind == txnWriteAPSequence {
		planner.appendAPWriteSequence(0, *op)
	} else {
		planner.appendAP(0, *op)
	}
	err := (&swdTxn{Txn: txn}).execute(ctx, planner.steps)
	txn.recordSWDCompletions()
	*op = txn.ops[0]
	op.result.err = classifyPortError(op.result.err)
	return op.result.value, classifyPortError(err)
}

func (dp *DebugPort) writeSWDSequence(ctx context.Context, op *txnOp) error {
	for _, value := range op.values {
		part := *op
		part.values = []uint32{value}
		part.result = &txnResult{}
		part.accepted, part.confirmed, part.uncertainWrite = 0, 0, false
		_, err := dp.executeSWDOp(ctx, &part)
		op.accepted += part.accepted
		op.confirmed += part.confirmed
		if err != nil {
			op.uncertainWrite = part.uncertainWrite
			return err
		}
	}
	return nil
}
