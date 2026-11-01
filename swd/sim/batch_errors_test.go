package sim_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

func TestPackedChunkPreservesStickyExemptResults(t *testing.T) {
	for _, failure := range []error{swd.ErrWait, swd.ErrFault} {
		for _, operation := range []string{"DPIDR", "CTRL/STAT", "ABORT"} {
			t.Run(failure.Error()+"/"+operation, func(t *testing.T) {
				checkStickyExemptBatch(t, failure, operation)
			})
		}
	}
}

type stickyBatchFixture struct {
	conn       *swd.Conn
	target     *connectionTarget
	wire       *packedCountingWire
	abortStart int
}

func openStickyBatch(t *testing.T, failure error, operation string) stickyBatchFixture {
	t.Helper()
	target := &connectionTarget{dpidr: 0x2ba01477}
	inner := sim.New(target)
	wire := &packedCountingWire{inner: inner}
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = inner.SetMaxTransferBits(0)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if failure == swd.ErrFault {
			if err := conn.WriteDP(ctx, 0, 0x1e); err != nil {
				t.Error(err)
			}
		}
		if err := conn.Release(ctx); err != nil {
			t.Error(err)
		}
	})
	limit := 108
	if operation == "ABORT" {
		limit = 216
	}
	if err := inner.SetMaxTransferBits(limit); err != nil {
		t.Fatal(err)
	}
	target.enforceSticky = true
	if failure == swd.ErrWait {
		target.waitAP = 1
	} else {
		target.ctrlStat |= testWriteDataErr
	}
	wire.calls = nil
	return stickyBatchFixture{conn: conn, target: target, wire: wire, abortStart: len(target.abortWrites)}
}

func checkStickyExemptBatch(t *testing.T, failure error, operation string) {
	t.Helper()
	fixture := openStickyBatch(t, failure, operation)
	batch := fixture.conn.NewBatch()
	failed := batch.ReadAP(0)
	var read *swd.ReadResult
	var write *swd.WriteResult
	switch operation {
	case "DPIDR":
		read = batch.ReadDP(0)
	case "CTRL/STAT":
		read = batch.ReadDP(4)
	case "ABORT":
		write = batch.WriteDP(0, 0x1e)
	}
	unsent := batch.ReadAP(0)
	if err := batch.Commit(t.Context()); !errors.Is(err, failure) || errors.Is(err, swd.ErrIndeterminate) {
		t.Errorf("Commit=%v", err)
	}
	if _, err := failed.Value(); !errors.Is(err, failure) {
		t.Errorf("failed result=%v", err)
	}
	if read != nil {
		checkStickyExemptRead(t, read, operation)
	}
	if write != nil && write.Err() != nil {
		t.Errorf("sticky-exempt ABORT=%v", write.Err())
	}
	if _, err := unsent.Value(); !errors.Is(err, swd.ErrNotExecuted) {
		t.Errorf("unsent result=%v", err)
	}
	var want []uint32
	wantCalls := []int{108}
	if operation == "ABORT" {
		want = append(want, 0x1e)
	}
	if failure == swd.ErrWait {
		want = append(want, 1<<4)
		wantCalls = append(wantCalls, 54)
	}
	if !slices.Equal(fixture.target.abortWrites[fixture.abortStart:], want) {
		t.Errorf("ABORT writes=%x want%x", fixture.target.abortWrites[fixture.abortStart:], want)
	}
	if !slices.Equal(fixture.wire.calls, wantCalls) {
		t.Errorf("calls=%v want%v", fixture.wire.calls, wantCalls)
	}
	if failure == swd.ErrWait && fixture.target.ctrlStat&testStickyOverrun != 0 {
		t.Error("WAIT cleanup retained STICKYORUN")
	}
}

func checkStickyExemptRead(t *testing.T, read *swd.ReadResult, operation string) {
	t.Helper()
	value, err := read.Value()
	if err != nil {
		t.Errorf("sticky-exempt read=%#x %v", value, err)
	}
	if operation == "DPIDR" && value != 0x2ba01477 {
		t.Errorf("DPIDR=%#x", value)
	}
	if operation == "CTRL/STAT" && value&testOverrunDetect == 0 {
		t.Errorf("CTRL/STAT=%#x", value)
	}
}

type selectWAITTarget struct {
	*connectionTarget
	waitSELECT bool
}

func (target *selectWAITTarget) Acknowledge(ctx context.Context, req sim.Request) error {
	if target.waitSELECT && !req.AP && !req.Read && req.Addr == 8 {
		target.waitSELECT = false
		return swd.ErrWait
	}
	return target.connectionTarget.Acknowledge(ctx, req)
}

func TestPackedChunkKeepsActualBankAfterRejectedSELECT(t *testing.T) {
	target := &selectWAITTarget{connectionTarget: &connectionTarget{dpidr: 0x2ba01477}}
	inner := sim.New(target)
	conn := swd.New(inner)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Release(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	target.enforceSticky = true
	target.waitSELECT = true
	batch := conn.NewBatch()
	selected := batch.WriteDP(8, 1)
	ctrl := batch.ReadDP(4)
	if err := batch.Commit(t.Context()); !errors.Is(err, swd.ErrWait) || errors.Is(err, swd.ErrIndeterminate) {
		t.Errorf("Commit=%v", err)
	}
	if err := selected.Err(); !errors.Is(err, swd.ErrWait) {
		t.Errorf("SELECT=%v", err)
	}
	if value, err := ctrl.Value(); err != nil || value != testOverrunDetect|testStickyOverrun {
		t.Errorf("actual CTRL/STAT=%#x %v", value, err)
	}
	if target.selectDP != 0 || target.ctrlStat&testStickyOverrun != 0 {
		t.Fatalf("SELECT=%#x CTRL/STAT=%#x", target.selectDP, target.ctrlStat)
	}
	if _, err := conn.ReadDP(t.Context(), 0); err != nil {
		t.Errorf("healthy stream became unusable: %v", err)
	}
}
