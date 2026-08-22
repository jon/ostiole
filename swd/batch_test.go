package swd

import (
	"context"
	"errors"
	"slices"
	"testing"
)

const testFixedFrameBits = 54

type batchWire struct {
	limit         int
	acks          []byte
	values        []uint32
	corruptParity map[int]bool
	calls         []int
	frames        int
	failCall      int
	err           error
	cancel        context.CancelFunc
}

type cancelWire struct {
	inner  Wire
	cancel context.CancelFunc
	calls  int
	after  int
}

type repeatedTargetWire struct {
	target targetWire
	calls  int
}

func (w *repeatedTargetWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if w.target.calls == 2 {
		w.target.calls = 0
	}
	w.calls++
	return w.target.SWDIO(ctx, direction, output, bits)
}

func (w *cancelWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	if err == nil && w.calls == w.after {
		w.cancel()
	}
	return input, err
}

func (w *batchWire) MaxTransferBits() int { return w.limit }

func (w *batchWire) SWDIO(_ context.Context, _, _ []byte, bits int) ([]byte, error) {
	w.calls = append(w.calls, bits)
	if len(w.calls) == w.failCall {
		if w.err != nil {
			return nil, w.err
		}
		return nil, errors.New("injected transport failure")
	}
	input := make([]byte, (bits+7)/8)
	for offset := 0; offset < bits; offset += testFixedFrameBits {
		index := w.frames
		w.frames++
		ack := byte(0b001)
		if index < len(w.acks) {
			ack = w.acks[index]
		}
		for bit := range 3 {
			setBit(input, offset+9+bit, ack>>uint(bit)&1 != 0)
		}
		if ack == 0b001 && index < len(w.values) {
			for bit := range 32 {
				setBit(input, offset+12+bit, w.values[index]>>uint(bit)&1 != 0)
			}
			setBit(input, offset+44, parity32(w.values[index]) != w.corruptParity[index])
		}
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	return input, nil
}

func TestBatchKeepsPackedPrefixWhenContextIsCanceledBetweenChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	wire := &batchWire{limit: 108, values: []uint32{1, 2, 3}, cancel: cancel}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	reads := []*ReadResult{batch.ReadDP(0x00), batch.ReadDP(0x00), batch.ReadDP(0x00)}
	if err := batch.Commit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit() error = %v, want context cancellation", err)
	}
	for i := range 2 {
		value, err := reads[i].Value()
		if value != uint32(i+1) || err != nil {
			t.Fatalf("read %d = %#x, %v; want %#x", i, value, err, i+1)
		}
	}
	if _, err := reads[2].Value(); !errors.Is(err, ErrNotExecuted) {
		t.Fatalf("third read error = %v, want ErrNotExecuted", err)
	}
	if !slices.Equal(wire.calls, []int{108}) {
		t.Fatalf("SWDIO bit counts = %v, want [108]", wire.calls)
	}
	if conn.state != connectionReady {
		t.Fatalf("connection state = %v, want ready", conn.state)
	}
}

func TestBatchStopsAfterCTRLSTATInvalidatesPendingSELECT(t *testing.T) {
	wire := &batchWire{limit: 162, values: []uint32{0, writeDataErr}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	conn.bank = bankSelection{bank: 0, valid: true}
	batch := conn.NewBatch()
	selectWrite := batch.WriteDP(0x08, 0)
	ctrlStat := batch.ReadDP(0x04)
	bankedWrite := batch.WriteDP(0x04, 1)
	if err := batch.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded after CTRL/STAT invalidated SELECT")
	}
	if err := selectWrite.Err(); err != nil {
		t.Fatalf("SELECT result error = %v", err)
	}
	if value, err := ctrlStat.Value(); value != writeDataErr || err != nil {
		t.Fatalf("CTRL/STAT result = %#x, %v; want %#x", value, err, writeDataErr)
	}
	if err := bankedWrite.Err(); err == nil {
		t.Fatal("banked write succeeded with an unknown DP bank")
	}
	if !slices.Equal(wire.calls, []int{108}) {
		t.Fatalf("SWDIO bit counts = %v, want [108]", wire.calls)
	}
}

func TestBatchAttributesPackedReadParity(t *testing.T) {
	wire := &batchWire{limit: 162, values: []uint32{1, 2, 3}, corruptParity: map[int]bool{1: true}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	reads := []*ReadResult{batch.ReadDP(0x00), batch.ReadDP(0x00), batch.ReadDP(0x00)}
	if err := batch.Commit(t.Context()); !errors.Is(err, ErrParity) {
		t.Fatalf("Commit() error = %v, want ErrParity", err)
	}
	for i, result := range reads {
		value, err := result.Value()
		if i == 1 {
			if !errors.Is(err, ErrParity) {
				t.Fatalf("read %d error = %v, want ErrParity", i, err)
			}
			continue
		}
		if value != uint32(i+1) || err != nil {
			t.Fatalf("read %d = %#x, %v; want %#x", i, value, err, i+1)
		}
	}
	if conn.state != connectionReady {
		t.Fatalf("connection state = %v, want ready", conn.state)
	}
}

func TestBatchRequiresRepairAfterPackedInvalidACK(t *testing.T) {
	wire := &batchWire{limit: 162, acks: []byte{0b001, 0b000, 0b001}, values: []uint32{1, 2, 3}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	reads := []*ReadResult{batch.ReadDP(0x00), batch.ReadDP(0x00), batch.ReadDP(0x00)}
	if err := batch.Commit(t.Context()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Commit() error = %v, want ErrProtocol", err)
	}
	if value, err := reads[0].Value(); value != 1 || err != nil {
		t.Fatalf("first read = %#x, %v; want 0x1", value, err)
	}
	for i := 1; i < len(reads); i++ {
		if _, err := reads[i].Value(); !errors.Is(err, ErrIndeterminate) {
			t.Fatalf("read %d error = %v, want ErrIndeterminate", i, err)
		}
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded after packed invalid ACK")
	}
}

func TestBatchPacksCompleteFramesUsingConnectionGrammar(t *testing.T) {
	wire := &batchWire{limit: 108, values: []uint32{1, 3, 5}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	reads := []*ReadResult{batch.ReadDP(0x00), batch.ReadAP(0x00), batch.ReadDP(0x08)}
	writes := []*WriteResult{batch.WriteDP(0x04, 1), batch.WriteAP(0x04, 0x55667788)}
	if err := batch.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(wire.calls, []int{108, 108, 54}) {
		t.Fatalf("SWDIO bit counts = %v", wire.calls)
	}
	for i, want := range []uint32{1, 3, 5} {
		value, err := reads[i].Value()
		if value != want || err != nil {
			t.Fatalf("read %d = %#x, %v; want %#x", i, value, err, want)
		}
	}
	for i := range writes {
		if err := writes[i].Err(); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
}

func TestBatchKeepsSimplePrefixWhenContextIsCanceledBetweenOperations(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	wire := &cancelWire{inner: &targetWire{ack: 0b001, readValue: 1}, cancel: cancel, after: 2}
	conn := readyConn(wire)
	batch := conn.NewBatch()
	first := batch.ReadDP(0x00)
	second := batch.ReadDP(0x00)
	if err := batch.Commit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit() error = %v, want context cancellation", err)
	}
	if value, err := first.Value(); value != 1 || err != nil {
		t.Fatalf("first read = %#x, %v; want 0x1", value, err)
	}
	if _, err := second.Value(); !errors.Is(err, ErrNotExecuted) {
		t.Fatalf("second read error = %v, want ErrNotExecuted", err)
	}
	if wire.calls != 2 {
		t.Fatalf("SWDIO calls = %d, want 2", wire.calls)
	}
	if conn.state != connectionReady {
		t.Fatalf("connection state = %v, want ready", conn.state)
	}
}

func TestBatchStopsSimpleTransfersAfterCTRLSTATInvalidatesPendingSELECT(t *testing.T) {
	wire := &repeatedTargetWire{target: targetWire{ack: 0b001, readValue: writeDataErr}}
	conn := readyConn(wire)
	conn.bank = bankSelection{bank: 0, valid: true}
	batch := conn.NewBatch()
	selectWrite := batch.WriteDP(0x08, 0)
	ctrlStat := batch.ReadDP(0x04)
	bankedWrite := batch.WriteDP(0x04, 0)
	if err := batch.Commit(t.Context()); err == nil || errors.Is(err, ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want determinate bank-selection error", err)
	}
	if err := selectWrite.Err(); err != nil {
		t.Fatalf("SELECT result error = %v", err)
	}
	if value, err := ctrlStat.Value(); value != writeDataErr || err != nil {
		t.Fatalf("CTRL/STAT result = %#x, %v; want %#x", value, err, writeDataErr)
	}
	if err := bankedWrite.Err(); err == nil || errors.Is(err, ErrIndeterminate) {
		t.Fatalf("banked write error = %v, want determinate bank-selection error", err)
	}
	if wire.calls != 4 {
		t.Fatalf("SWDIO calls = %d, want 4", wire.calls)
	}
	if conn.state != connectionReady {
		t.Fatalf("connection state = %v, want ready", conn.state)
	}
}

func TestBatchUsesOneFramePerCallWithoutWireLimit(t *testing.T) {
	inner := &batchWire{limit: 108, values: []uint32{1, 2}}
	wire := struct{ Wire }{Wire: inner}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	batch.ReadDP(0x00)
	batch.ReadDP(0x00)
	if err := batch.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inner.calls, []int{54, 54}) {
		t.Fatalf("SWDIO bit counts = %v", inner.calls)
	}
}

func TestBatchAttributesTransportFailureToPhysicalChunk(t *testing.T) {
	wire := &batchWire{limit: 108, values: []uint32{1, 2}, failCall: 2}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	results := []*ReadResult{batch.ReadDP(0x00), batch.ReadDP(0x00), batch.ReadAP(0x00), batch.ReadAP(0x00), batch.ReadDP(0x00)}
	if err := batch.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded after a transport failure")
	}
	for i, result := range results {
		_, err := result.Value()
		switch {
		case i < 2 && err != nil:
			t.Fatalf("confirmed result %d error = %v", i, err)
		case i >= 2 && i < 4 && !errors.Is(err, ErrIndeterminate):
			t.Fatalf("clocked result %d error = %v, want ErrIndeterminate", i, err)
		case i == 4 && !errors.Is(err, ErrNotExecuted):
			t.Fatalf("unsent result error = %v, want ErrNotExecuted", err)
		}
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded after batch transport failure")
	}
}

func TestBatchRequiresRepairWhenRequestAfterWAITCompletes(t *testing.T) {
	wire := &batchWire{limit: 108, acks: []byte{0b010, 0b001}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	waited := batch.ReadAP(0x00)
	unexpected := batch.ReadAP(0x00)
	if err := batch.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded after a request following WAIT was accepted")
	}
	if _, err := waited.Value(); !errors.Is(err, ErrWait) {
		t.Fatalf("WAITed result error = %v, want ErrWait", err)
	}
	if _, err := unexpected.Value(); !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("unexpected result error = %v, want ErrIndeterminate", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded after inconsistent packed responses")
	}
}

func TestBatchPreservesPackedWAITCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("injected STICKYORUN cleanup failure")
	wire := &batchWire{limit: 108, acks: []byte{0b010, 0b100}, failCall: 2, err: cleanupErr}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	waited := batch.ReadAP(0x00)
	abandoned := batch.ReadAP(0x00)
	if err := batch.Commit(t.Context()); !errors.Is(err, ErrWait) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Commit() error = %v, want WAIT and cleanup failure", err)
	}
	if _, err := waited.Value(); !errors.Is(err, ErrWait) || !errors.Is(err, cleanupErr) {
		t.Fatalf("WAITed result error = %v, want WAIT and cleanup failure", err)
	}
	if _, err := abandoned.Value(); !errors.Is(err, ErrFault) {
		t.Fatalf("abandoned result error = %v, want ErrFault", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded after packed WAIT cleanup failure")
	}
}

func TestBatchPreservesPackedWAITWhenCleanupABORTReturnsWAIT(t *testing.T) {
	wire := &batchWire{limit: 108, acks: []byte{0b010, 0b100, 0b010}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	waited := batch.ReadAP(0x00)
	batch.ReadAP(0x00)
	if err := batch.Commit(t.Context()); err == ErrWait || !errors.Is(err, ErrWait) {
		t.Fatalf("Commit() error = %v, want WAIT with cleanup failure", err)
	}
	if _, err := waited.Value(); err == ErrWait || !errors.Is(err, ErrWait) {
		t.Fatalf("WAITed result error = %v, want WAIT with cleanup failure", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded after cleanup ABORT returned WAIT")
	}
}

func TestBatchPreservesWAITFromRequestedABORT(t *testing.T) {
	wire := &batchWire{limit: 54, acks: []byte{0b010}}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	abort := batch.WriteDP(0x00, 0)
	if err := batch.Commit(t.Context()); err == ErrWait || !errors.Is(err, ErrWait) {
		t.Fatalf("Commit() error = %v, want WAIT with unavailable cleanup", err)
	}
	if err := abort.Err(); err == ErrWait || !errors.Is(err, ErrWait) {
		t.Fatalf("ABORT result error = %v, want WAIT with unavailable cleanup", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded after requested ABORT returned WAIT")
	}
}

func TestBatchRejectsShortWireLimitBeforeTraffic(t *testing.T) {
	wire := &batchWire{limit: 53}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	result := batch.ReadDP(0x00)
	if err := batch.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded with a short wire limit")
	}
	if len(wire.calls) != 0 {
		t.Fatalf("SWDIO calls = %v, want none", wire.calls)
	}
	if _, err := result.Value(); !errors.Is(err, ErrNotExecuted) {
		t.Fatalf("result error = %v, want ErrNotExecuted", err)
	}
}

func TestBatchValidatesCompleteQueueBeforeTraffic(t *testing.T) {
	wire := &batchWire{limit: 108}
	conn := readyConn(wire)
	conn.response = responseOverrun
	batch := conn.NewBatch()
	valid := batch.ReadDP(0x00)
	invalid := batch.WriteAP(0x03, 1)
	if err := batch.Commit(t.Context()); err == nil {
		t.Fatal("Commit() succeeded with an invalid address")
	}
	if len(wire.calls) != 0 {
		t.Fatalf("SWDIO calls = %v, want none", wire.calls)
	}
	if _, err := valid.Value(); !errors.Is(err, ErrNotExecuted) {
		t.Fatalf("valid result error = %v, want ErrNotExecuted", err)
	}
	if err := invalid.Err(); err == nil || errors.Is(err, ErrNotExecuted) {
		t.Fatalf("invalid result error = %v, want address error", err)
	}
}

func TestBatchResultsArePendingAndSingleUse(t *testing.T) {
	conn := readyConn(&batchWire{limit: 54, values: []uint32{1}})
	conn.response = responseOverrun
	batch := conn.NewBatch()
	read := batch.ReadDP(0x00)
	write := batch.WriteAP(0x0c, 1)
	if _, err := read.Value(); !errors.Is(err, ErrResultPending) {
		t.Fatalf("Value() before Commit error = %v, want ErrResultPending", err)
	}
	if err := write.Err(); !errors.Is(err, ErrResultPending) {
		t.Fatalf("Err() before Commit = %v, want ErrResultPending", err)
	}
	if err := batch.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(t.Context()); !errors.Is(err, ErrBatchCommitted) {
		t.Fatalf("second Commit() error = %v, want ErrBatchCommitted", err)
	}
	if _, err := batch.ReadDP(0x00).Value(); !errors.Is(err, ErrBatchCommitted) {
		t.Fatalf("queued-after-Commit result error = %v, want ErrBatchCommitted", err)
	}
	if _, err := (*ReadResult)(nil).Value(); !errors.Is(err, ErrResultPending) {
		t.Fatalf("nil ReadResult error = %v, want ErrResultPending", err)
	}
	if err := (*WriteResult)(nil).Err(); !errors.Is(err, ErrResultPending) {
		t.Fatalf("nil WriteResult error = %v, want ErrResultPending", err)
	}
}

func TestBatchStopsSimpleTransfersAtFirstError(t *testing.T) {
	wire := &targetWire{ack: 0b010}
	conn := readyConn(wire)
	batch := conn.NewBatch()
	failed := batch.ReadDP(0x00)
	unsent := batch.ReadDP(0x00)
	if err := batch.Commit(t.Context()); !errors.Is(err, ErrWait) {
		t.Fatalf("Commit() error = %v, want ErrWait", err)
	}
	if _, err := failed.Value(); !errors.Is(err, ErrWait) {
		t.Fatalf("failed result error = %v, want ErrWait", err)
	}
	if _, err := unsent.Value(); !errors.Is(err, ErrNotExecuted) {
		t.Fatalf("unsent result error = %v, want ErrNotExecuted", err)
	}
}
