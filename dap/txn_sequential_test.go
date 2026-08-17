package dap

import "testing"

func TestTxnPlannerPipelinesSequentialAPReads(t *testing.T) {
	dp := &DebugPort{}
	sel := NewAPSel(2)
	ops := []txnOp{
		{kind: txnReadAPSequential, apSel: sel, apAddr: memAPDRW},
		{kind: txnReadAPSequential, apSel: sel, apAddr: memAPDRW},
		{kind: txnReadAPSequential, apSel: sel, apAddr: memAPDRW},
	}
	steps := newTxnPlanner(dp).plan(ops)
	if len(steps) != 6 {
		t.Fatalf("pipeline steps = %+v, want SELECT, its barrier, three AP reads, and RDBUFF", steps)
	}
	for i := 2; i <= 4; i++ {
		if steps[i].req != apTransferRequest(0x0c, true) {
			t.Fatalf("step %d = %+v, want AP DRW read", i, steps[i])
		}
		if !steps[i].apRead || i > 2 && !steps[i].operationStarted {
			t.Fatalf("step %d does not record its posted-read effect: %+v", i, steps[i])
		}
	}
	if steps[5].req != dpTransferRequest(RDBUFF, true) {
		t.Fatalf("last step = %+v, want RDBUFF", steps[5])
	}
	if !steps[5].apRead || !steps[5].operationStarted {
		t.Fatalf("RDBUFF does not record the posted read it completes: %+v", steps[5])
	}
	if steps[2].deliver || !steps[3].deliver || steps[3].op != 0 || !steps[4].deliver || steps[4].op != 1 || !steps[5].deliver || steps[5].op != 2 {
		t.Fatalf("pipeline result attribution = %+v", steps)
	}
}

func TestTxnPlannerBuffersAPWriteSequence(t *testing.T) {
	dp := &DebugPort{}
	ops := []txnOp{{kind: txnWriteAPSequence, apSel: NewAPSel(3), apAddr: memAPDRW, values: []uint32{1, 2, 3}}}
	steps := newTxnPlanner(dp).plan(ops)
	if len(steps) != 6 {
		t.Fatalf("write steps = %+v, want SELECT, its barrier, three AP writes, and RDBUFF", steps)
	}
	for i := 2; i <= 4; i++ {
		if steps[i].req != apTransferRequest(0x0c, false) || steps[i].data != uint32(i-1) || !steps[i].acceptWrite {
			t.Fatalf("step %d = %+v, want buffered AP DRW write %d", i, steps[i], i-1)
		}
	}
	if steps[5].req != dpTransferRequest(RDBUFF, true) || !steps[5].deliver || !steps[5].completesWrite {
		t.Fatalf("last step = %+v, want completing RDBUFF", steps[5])
	}
}
