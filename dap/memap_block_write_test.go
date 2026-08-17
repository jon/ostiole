package dap_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type parityAfterDRWTarget struct {
	*sim.Target
	wire *readParityWire
}

type observedBlockWriteTarget struct {
	*waitTarget
	cancel      context.CancelFunc
	cancelAfter int
	dropAfter   int
	failAfter   int
	failErr     error
	waitAfter   int
	waitCount   int
	writes      int
}

type simpleObservedBlockWriteTarget struct {
	*observedBlockWriteTarget
}

type stagedCancelBlockWriteTarget struct {
	*simpleObservedBlockWriteTarget
	ctx   *stagedCancelContext
	after int
}

type blockWriteWAITStage struct {
	name        string
	waitFor     swdsim.Request
	afterWrites int
	addr        uint64
	data        []byte
}

func (t *parityAfterDRWTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if err := t.Target.Write(ctx, req, value); err != nil {
		return err
	}
	if req == apWrite(0x0c) {
		t.wire.armed = true
	}
	return nil
}

func (t *observedBlockWriteTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req != apWrite(0x0c) {
		return t.waitTarget.Write(ctx, req, value)
	}
	t.writes++
	if t.writes == t.dropAfter {
		t.dropWriteFor = req
		t.dropWrite = true
		t.stickyOnDrop = testWriteDataError
	}
	err := t.waitTarget.Write(ctx, req, value)
	if err != nil {
		return err
	}
	if t.writes == t.cancelAfter && t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if t.writes == t.waitAfter {
		waits := t.waitCount
		if waits == 0 {
			waits = 1
		}
		t.arm(dpRead(0x0c), waits)
	}
	if t.writes == t.failAfter {
		return t.failErr
	}
	return nil
}

func (t *simpleObservedBlockWriteTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req == dpWrite(0x04) {
		value &^= overrunDetect
	}
	return t.observedBlockWriteTarget.Write(ctx, req, value)
}

func (t *stagedCancelBlockWriteTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	err := t.waitTarget.Acknowledge(ctx, req)
	if errors.Is(err, swd.ErrWait) && !t.ctx.armed && t.attempts == t.after {
		t.ctx.armed = true
	}
	return err
}

func TestMEMAPWritesArbitraryBlocksInBothByteOrders(t *testing.T) {
	tests := []struct {
		name string
		cfg  uint32
	}{
		{name: "little endian"},
		{name: "big endian", cfg: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := sim.New(0x2ba01477)
			addMEMAP(t, target, 0, 0x00010001, nil)
			if err := target.SetMEMAPCFG(apSel(0), test.cfg); err != nil {
				t.Fatal(err)
			}
			mem, err := dap.OpenMemAP(t.Context(), enteredDAPClient(t, target), apSel(0))
			if err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 2056)
			for i := range data {
				data[i] = byte(i*53 + 7)
			}
			n, err := mem.WriteBlock(t.Context(), 0x3fd, data)
			if err != nil || n != len(data) {
				t.Fatalf("WriteBlock() = %d, %v, want %d, nil", n, err, len(data))
			}
			got, err := target.MEMAPBytes(apSel(0), 0x3fd, len(data))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, data) {
				t.Fatalf("target bytes = % x, want % x", got, data)
			}
		})
	}
}

func TestMEMAPWriteBlockFallsBackWithoutSingleAddressIncrement(t *testing.T) {
	target := &noAddressIncrementTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target, 0, 0x00010001, nil)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	n, err := mem.WriteBlock(t.Context(), 0, data)
	if err != nil || n != len(data) {
		t.Fatalf("WriteBlock() without address increment = %d, %v; want %d, nil", n, err, len(data))
	}
	got, err := target.MEMAPBytes(apSel(0), 0, len(data))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, data) {
		t.Fatalf("target bytes = % x, want % x", got, data)
	}
	if got := target.executed[apWrite(0x04)]; got != len(data)/4 {
		t.Fatalf("TAR writes = %d, want %d", got, len(data)/4)
	}
}

func TestMEMAPWriteBlockFallbackReturnsCompletedPrefix(t *testing.T) {
	target := &noAddressIncrementTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target, 0, 0x00010001, nil)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.armFaultAfter(apWrite(0x0c), 1)
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	n, err := mem.WriteBlock(t.Context(), 0, data)
	if !errors.Is(err, swd.ErrFault) || n != 4 {
		t.Fatalf("WriteBlock() fallback = %d, %v; want 4, FAULT", n, err)
	}
	got, err := target.MEMAPBytes(apSel(0), 0, len(data))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[:n], data[:n]) {
		t.Fatalf("target prefix = % x, want % x", got[:n], data[:n])
	}
	if !slices.Equal(got[n:], make([]byte, len(data)-n)) {
		t.Fatalf("target suffix = % x, want zeros", got[n:])
	}
}

func TestMEMAPWriteBlockCountsOnlyCompletedChunks(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, nil)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 68*4)
	for i := range data {
		data[i] = byte(i + 1)
	}
	target.armFaultAfter(apWrite(0x0c), 66)
	n, err := mem.WriteBlock(t.Context(), 0, data)
	if !errors.Is(err, swd.ErrFault) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("WriteBlock() error = %v, want FAULT and indeterminate effect", err)
	}
	if n != 64*4 {
		t.Fatalf("WriteBlock() prefix = %d, want one completed 64-word chunk", n)
	}
	got, err := target.MEMAPBytes(apSel(0), 0, n)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, data[:n]) {
		t.Fatal("WriteBlock() did not write its reported prefix")
	}
}

func TestMEMAPWriteBlockInvalidatesStateWhenWDATAERRMakesChunkIndeterminate(t *testing.T) {
	target := &observedBlockWriteTarget{waitTarget: newWaitTarget(), dropAfter: 3}
	mem, wire := openObservedBlockWriteMEMAP(t, target)
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	n, err := mem.WriteBlock(t.Context(), 0, data)
	if !errors.Is(err, swd.ErrFault) || !errors.Is(err, dap.ErrIndeterminate) || n != 0 {
		t.Fatalf("WriteBlock() = %d, %v, want 0, FAULT, and ErrIndeterminate", n, err)
	}
	assertBlockedMEMAPUsesNoWire(t, mem, wire)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after indeterminate WDATAERR: %v", err)
	}
}

func TestMEMAPWriteBlockRetainsCompletedChunkAfterCancellation(t *testing.T) {
	target := &observedBlockWriteTarget{waitTarget: newWaitTarget(), cancelAfter: 66}
	mem, wire := openObservedBlockWriteMEMAP(t, target)
	ctx, cancel := context.WithCancel(t.Context())
	target.cancel = cancel
	data := blockWriteTestData(68 * 4)
	n, err := mem.WriteBlock(ctx, 0, data)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, dap.ErrIndeterminate) || n != 64*4 {
		t.Fatalf("WriteBlock() = %d, %v, want %d, context cancellation, and ErrIndeterminate", n, err, 64*4)
	}
	assertBlockedMEMAPUsesNoWire(t, mem, wire)
	if err := mem.Release(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Release() with canceled context = %v, want context cancellation", err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("retry Release() after block-write cancellation: %v", err)
	}
}

func TestMEMAPWriteBlockRetainsCompletedChunkAfterTransportFailure(t *testing.T) {
	transportErr := errors.New("block write transport failed")
	target := &observedBlockWriteTarget{waitTarget: newWaitTarget(), failAfter: 66, failErr: transportErr}
	mem, wire := openObservedBlockWriteMEMAP(t, target)
	data := blockWriteTestData(68 * 4)
	n, err := mem.WriteBlock(t.Context(), 0, data)
	if !errors.Is(err, transportErr) || !errors.Is(err, dap.ErrIndeterminate) || n != 64*4 {
		t.Fatalf("WriteBlock() = %d, %v, want %d, transport failure, and ErrIndeterminate", n, err, 64*4)
	}
	assertBlockedMEMAPUsesNoWire(t, mem, wire)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after block-write transport failure: %v", err)
	}
}

func TestMEMAPWriteBlockRetriesOnlyTheWAITedCompletion(t *testing.T) {
	target := &observedBlockWriteTarget{waitTarget: newWaitTarget(), waitAfter: 4}
	mem, _ := openObservedBlockWriteMEMAP(t, target)
	beforeWrites := countRequests(target.requests, apWrite(0x0c))
	data := blockWriteTestData(16)
	n, err := mem.WriteBlock(t.Context(), 0, data)
	if err != nil || n != len(data) {
		t.Fatalf("WriteBlock() = %d, %v, want %d, nil", n, err, len(data))
	}
	if got := countRequests(target.requests, apWrite(0x0c)) - beforeWrites; got != 4 {
		t.Fatalf("DRW write requests = %d, want 4", got)
	}
	if target.attempts != 2 {
		t.Fatalf("WAITed RDBUFF attempts = %d, want 2", target.attempts)
	}
}

func TestMEMAPWriteBlockRetriesWAITedRequestsUntilTargetResponds(t *testing.T) {
	stages := []blockWriteWAITStage{
		{name: "CSW write", waitFor: apWrite(0x00), data: blockWriteTestData(4)},
		{name: "CSW read", waitFor: apRead(0x00), data: blockWriteTestData(4)},
		{name: "TAR write", waitFor: apWrite(0x04), data: blockWriteTestData(4)},
		{name: "word write", waitFor: apWrite(0x0c), data: blockWriteTestData(16)},
		{name: "completion", afterWrites: 4, data: blockWriteTestData(16)},
		{name: "byte edge", waitFor: apWrite(0x0c), addr: 1, data: blockWriteTestData(1)},
	}
	for _, grammar := range []struct {
		name   string
		simple bool
	}{{name: "overrun"}, {name: "simple", simple: true}} {
		t.Run(grammar.name, func(t *testing.T) {
			for _, stage := range stages {
				t.Run(stage.name, func(t *testing.T) { testMEMAPWriteBlockWAIT(t, grammar.simple, stage) })
			}
		})
	}
}

func testMEMAPWriteBlockWAIT(t *testing.T, simple bool, stage blockWriteWAITStage) {
	t.Helper()
	target := &observedBlockWriteTarget{waitTarget: newWaitTarget()}
	var wireTarget swdsim.Target = target
	if simple {
		wireTarget = &simpleObservedBlockWriteTarget{observedBlockWriteTarget: target}
	}
	addMEMAP(t, target, 0, 0x00010001, nil)
	dp := newDebugPort(t, wireTarget)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if stage.afterWrites != 0 {
		target.waitAfter = stage.afterWrites
		target.waitCount = 101
	} else {
		target.arm(stage.waitFor, 101)
	}
	beforeWrites := countRequests(target.requests, apWrite(0x0c))
	n, err := mem.WriteBlock(t.Context(), stage.addr, stage.data)
	if err != nil || n != len(stage.data) {
		t.Fatalf("WriteBlock() after 101 WAITs = %d, %v, want %d, nil", n, err, len(stage.data))
	}
	got, err := target.MEMAPBytes(apSel(0), stage.addr, len(stage.data))
	if err != nil || !slices.Equal(got, stage.data) {
		t.Fatalf("target bytes = % x, %v, want % x", got, err, stage.data)
	}
	if stage.afterWrites != 0 {
		if got := countRequests(target.requests, apWrite(0x0c)) - beforeWrites; got != stage.afterWrites {
			t.Fatalf("DRW write requests = %d, want %d", got, stage.afterWrites)
		}
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, want no DAPABORT while retrying the request", target.abortValues)
		}
	}
}

func TestMEMAPWriteBlockStopsRetryingCompletionWhenContextEnds(t *testing.T) {
	ctx := &stagedCancelContext{Context: t.Context(), done: make(chan struct{})}
	target := &observedBlockWriteTarget{waitTarget: newWaitTarget(), waitAfter: 4, waitCount: -1}
	addMEMAP(t, target, 0, 0x00010001, nil)
	wireTarget := &stagedCancelBlockWriteTarget{simpleObservedBlockWriteTarget: &simpleObservedBlockWriteTarget{observedBlockWriteTarget: target}, ctx: ctx, after: 101}
	wire := &packedTxnWire{inner: swdsim.New(wireTarget), limit: 54}
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	data := blockWriteTestData(16)
	n, err := mem.WriteBlock(ctx, 0, data)
	if !errors.Is(err, context.Canceled) || errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) || n != 0 {
		t.Fatalf("WriteBlock() after cancellation = %d, %v, want 0, context cancellation, and ErrIndeterminate", n, err)
	}
	if target.writes != 4 {
		t.Fatalf("DRW writes = %d, want 4 without replay", target.writes)
	}
	assertBlockReadDAPABORT(t, target.waitTarget)
	assertBlockedMEMAPUsesNoWire(t, mem, wire)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after canceled block-write completion: %v", err)
	}
}

func openObservedBlockWriteMEMAP(t *testing.T, target *observedBlockWriteTarget) (*dap.MemAP, *packedTxnWire) {
	t.Helper()
	addMEMAP(t, target, 0, 0x00010001, nil)
	wire := &packedTxnWire{inner: swdsim.New(target), limit: 54}
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	return mem, wire
}

func blockWriteTestData(length int) []byte {
	data := make([]byte, length)
	for i := range data {
		data[i] = byte(i*37 + 11)
	}
	return data
}

func TestMEMAPWriteBlockDistinguishesRejectedWriteFromRejectedCompletion(t *testing.T) {
	tests := []struct {
		name       string
		req        swdsim.Request
		successful int
		indet      bool
	}{
		{name: "first write rejected", req: apWrite(0x0c)},
		{name: "completion rejected", req: dpRead(0x0c), successful: 5, indet: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newWaitTarget()
			addMEMAP(t, target, 0, 0x00010001, nil)
			dp := newDebugPort(t, target)
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
			if err != nil {
				t.Fatal(err)
			}
			target.armFaultAfter(test.req, test.successful)
			n, err := mem.WriteBlock(t.Context(), 0, []byte{1, 2, 3, 4})
			if !errors.Is(err, swd.ErrFault) || errors.Is(err, dap.ErrIndeterminate) != test.indet || n != 0 {
				t.Fatalf("WriteBlock() = %d, %v, want FAULT indeterminate=%t", n, err, test.indet)
			}
		})
	}
}

func TestMEMAPWriteBlockCountsCompletedChunkWhenBarrierDataParityFails(t *testing.T) {
	target := &parityAfterDRWTarget{Target: sim.New(0x2ba01477)}
	addMEMAP(t, target, 0, 0x00010001, nil)
	wire := &readParityWire{inner: swdsim.New(target)}
	target.wire = wire
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = mem.Release(context.Background())
		_ = dp.Release(context.Background())
	})

	data := []byte{1, 2, 3, 4}
	n, err := mem.WriteBlock(t.Context(), 0, data)
	if !errors.Is(err, swd.ErrParity) || errors.Is(err, dap.ErrIndeterminate) || n != len(data) {
		t.Fatalf("WriteBlock() = %d, %v, want completed prefix and determinate parity error", n, err)
	}
	got, err := target.MEMAPBytes(apSel(0), 0, len(data))
	if err != nil || !slices.Equal(got, data) {
		t.Fatalf("target bytes = % x, %v, want % x", got, err, data)
	}
	value, err := mem.ReadScalar(t.Context(), 0, dap.Size32)
	if err != nil || value != 0x04030201 {
		t.Fatalf("ReadScalar() after parity error = %#x, %v", value, err)
	}
}

func TestMEMAPWriteBlockRetriesTheWAITedRequest(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, nil)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.arm(apWrite(0x0c), 1)
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	n, err := mem.WriteBlock(t.Context(), 0, data)
	if err != nil || n != len(data) {
		t.Fatalf("WriteBlock() after WAIT = %d, %v, want %d, nil", n, err, len(data))
	}
	got, err := target.MEMAPBytes(apSel(0), 0, len(data))
	if err != nil || !slices.Equal(got, data) {
		t.Fatalf("target bytes = % x, %v, want % x", got, err, data)
	}
}

func TestMEMAPWriteBlockValidatesBeforeMemoryTraffic(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, nil)
	if err := target.SetMEMAPSizes(apSel(0), dap.Size32); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	beforeDRW := countRequests(target.requests, apWrite(0x0c))
	if n, err := mem.WriteBlock(t.Context(), 1, []byte{1, 2, 3, 4}); err == nil || n != 0 {
		t.Fatalf("WriteBlock() = %d, %v on a word-only MEM-AP", n, err)
	}
	if n, err := mem.WriteBlock(t.Context(), ^uint64(0), []byte{1, 2}); err == nil || n != 0 {
		t.Fatalf("overflowing WriteBlock() = %d, %v", n, err)
	}
	afterDRW := countRequests(target.requests, apWrite(0x0c))
	if afterDRW != beforeDRW {
		t.Fatalf("DRW writes after validation = %d, want %d", afterDRW, beforeDRW)
	}
}

func TestEmptyMEMAPWriteBlockUsesNoPort(t *testing.T) {
	var mem *dap.MemAP
	if n, err := mem.WriteBlock(t.Context(), 0, nil); err != nil || n != 0 {
		t.Fatalf("WriteBlock(nil) = %d, %v", n, err)
	}
}
