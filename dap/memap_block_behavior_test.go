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

type failingBlockReadTarget struct {
	*waitTarget
	remaining int
	err       error
}

type cancelingBlockWAITTarget struct {
	*waitTarget
	cancel context.CancelFunc
	after  int
}

type simpleBlockTarget struct {
	*waitTarget
}

type noAddressIncrementTarget struct {
	*waitTarget
}

type stagedCancelContext struct {
	context.Context
	done     chan struct{}
	armed    bool
	checks   int
	canceled bool
}

type stagedCancelBlockWAITTarget struct {
	*simpleBlockTarget
	ctx *stagedCancelContext
}

type blockWAITStage struct {
	name    string
	waitFor swdsim.Request
	addr    uint64
	length  int
}

func (t *failingBlockReadTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	if t.err != nil && req == apRead(0x0c) {
		if t.remaining == 0 {
			return 0, t.err
		}
		t.remaining--
	}
	return t.waitTarget.Read(ctx, req)
}

func (t *cancelingBlockWAITTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	err := t.waitTarget.Acknowledge(ctx, req)
	if errors.Is(err, swd.ErrWait) && t.cancel != nil && t.attempts == t.after {
		t.cancel()
		t.cancel = nil
	}
	return err
}

func (t *simpleBlockTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req == dpWrite(0x04) {
		value &^= overrunDetect
	}
	return t.waitTarget.Write(ctx, req, value)
}

func (t *noAddressIncrementTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req == apWrite(0x00) {
		value &^= 0x30
	}
	return t.waitTarget.Write(ctx, req, value)
}

func (c *stagedCancelContext) Done() <-chan struct{} {
	return c.done
}

func (c *stagedCancelContext) Err() error {
	if !c.armed {
		return c.Context.Err()
	}
	c.checks++
	if c.checks == 1 {
		return nil
	}
	if !c.canceled {
		close(c.done)
		c.canceled = true
	}
	return context.Canceled
}

func (t *stagedCancelBlockWAITTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	err := t.waitTarget.Acknowledge(ctx, req)
	if errors.Is(err, swd.ErrWait) && !t.ctx.armed {
		t.ctx.armed = true
	}
	return err
}

type blockReadParityWire struct {
	inner swd.Wire
	armed bool
	calls int
}

func (w *blockReadParityWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	w.calls++
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	if err == nil && w.armed && bits == 54 && len(output) != 0 && output[0] == 0x9f {
		w.armed = false
		input[44/8] ^= 1 << (44 % 8)
	}
	return input, err
}

func TestMEMAPReadsArbitraryBlocksInBothByteOrders(t *testing.T) {
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
			data := make([]byte, 2056)
			for i := range data {
				address := 0x3fd + i
				data[i] = byte(i*37+11) ^ byte((address>>10)*0x53)
			}
			if err := target.SetMEMAPBytes(apSel(0), 0x3fd, data); err != nil {
				t.Fatal(err)
			}
			mem, err := dap.OpenMemAP(t.Context(), enteredDAPClient(t, target), apSel(0))
			if err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(data))
			n, err := mem.ReadBlock(t.Context(), 0x3fd, got)
			if err != nil || n != len(got) {
				t.Fatalf("ReadBlock() = %d, %v, want %d, nil", n, err, len(got))
			}
			if !slices.Equal(got, data) {
				t.Fatal("ReadBlock() did not return the target bytes across TAR windows")
			}
		})
	}
}

func TestMEMAPReadBlockReturnsOnlyConfirmedPrefix(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, nil)
	data := make([]byte, 20)
	for i := range data {
		data[i] = byte(i + 1)
	}
	if err := target.SetMEMAPBytes(apSel(0), 0, data); err != nil {
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
	target.arm(apRead(0x0c), 101)
	target.fault = true
	target.faultAfter = 3
	buf := make([]byte, len(data))
	for i := range buf {
		buf[i] = 0xee
	}
	n, err := mem.ReadBlock(t.Context(), 0, buf)
	if !errors.Is(err, swd.ErrFault) {
		t.Fatalf("ReadBlock() error = %v, want FAULT", err)
	}
	if n != 8 || !slices.Equal(buf[:n], data[:n]) {
		t.Fatalf("ReadBlock() prefix = %d, % x, want 8, % x", n, buf[:n], data[:n])
	}
	for i, value := range buf[n:] {
		if value != 0xee {
			t.Fatalf("ReadBlock() changed unread byte %d to %#x", n+i, value)
		}
	}
}

func TestMEMAPReadBlockFallsBackWithoutSingleAddressIncrement(t *testing.T) {
	target := &noAddressIncrementTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target, 0, 0x00010001, nil)
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	if err := target.SetMEMAPBytes(apSel(0), 0, data); err != nil {
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
	buf := make([]byte, len(data))
	n, err := mem.ReadBlock(t.Context(), 0, buf)
	if err != nil || n != len(buf) {
		t.Fatalf("ReadBlock() without address increment = %d, %v; want %d, nil", n, err, len(buf))
	}
	if !slices.Equal(buf, data) {
		t.Fatalf("ReadBlock() = % x, want % x", buf, data)
	}
	if got := target.executed[apWrite(0x04)]; got != len(data)/4 {
		t.Fatalf("TAR writes = %d, want %d", got, len(data)/4)
	}
}

func TestMEMAPReadBlockFallbackReturnsConfirmedPrefix(t *testing.T) {
	target := &noAddressIncrementTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target, 0, 0x00010001, nil)
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	if err := target.SetMEMAPBytes(apSel(0), 0, data); err != nil {
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
	target.arm(apRead(0x0c), 0)
	target.fault = true
	target.faultAfter = 2
	buf := []byte{0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee}
	n, err := mem.ReadBlock(t.Context(), 0, buf)
	if !errors.Is(err, swd.ErrFault) || n != 8 {
		t.Fatalf("ReadBlock() fallback = %d, %v; want 8, FAULT", n, err)
	}
	if !slices.Equal(buf[:n], data[:n]) {
		t.Fatalf("ReadBlock() prefix = % x, want % x", buf[:n], data[:n])
	}
	if !slices.Equal(buf[n:], []byte{0xee, 0xee, 0xee, 0xee}) {
		t.Fatalf("ReadBlock() unread suffix = % x, want ee ee ee ee", buf[n:])
	}
}

func TestMEMAPReadBlockInvalidatesStateAfterCanceledPostedRead(t *testing.T) {
	target := &cancelAPTarget{waitTarget: newWaitTarget(), req: apRead(0x0c)}
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
	ctx, cancel := context.WithCancel(t.Context())
	target.cancel = cancel
	buf := []byte{0xee, 0xee, 0xee, 0xee}
	n, err := mem.ReadBlock(ctx, 0, buf)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, dap.ErrIndeterminate) || n != 0 {
		t.Fatalf("ReadBlock() after posted-read cancellation = %d, %v, want 0, context cancellation, and ErrIndeterminate", n, err)
	}
	if !slices.Equal(buf, []byte{0xee, 0xee, 0xee, 0xee}) {
		t.Fatalf("ReadBlock() changed the destination after an unconfirmed read: % x", buf)
	}
	assertBlockedMEMAPUsesNoWire(t, mem, wire)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after posted-read cancellation: %v", err)
	}
}

func TestMEMAPReadBlockInvalidatesStateAfterPostedReadParityError(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, nil)
	wire := &blockReadParityWire{inner: swdsim.New(target)}
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	wire.armed = true
	buf := []byte{0xee, 0xee, 0xee, 0xee}
	n, err := mem.ReadBlock(t.Context(), 0, buf)
	if !errors.Is(err, swd.ErrParity) || !errors.Is(err, dap.ErrIndeterminate) || n != 0 {
		t.Fatalf("ReadBlock() after posted-read parity error = %d, %v, want 0, ErrParity, and ErrIndeterminate", n, err)
	}
	if !slices.Equal(buf, []byte{0xee, 0xee, 0xee, 0xee}) {
		t.Fatalf("ReadBlock() changed the destination after an unconfirmed read: % x", buf)
	}
	traffic := wire.calls
	if _, err := mem.ReadWord(t.Context(), 0); err == nil {
		t.Fatal("ReadWord() succeeded after posted-read parity error")
	}
	if wire.calls != traffic {
		t.Fatalf("blocked MEM-AP read added %d SWDIO calls", wire.calls-traffic)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after posted-read parity error: %v", err)
	}
}

func TestMEMAPReadBlockRetainsConfirmedPrefixAfterTransportFailure(t *testing.T) {
	target := &failingBlockReadTarget{waitTarget: newWaitTarget()}
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 0x03020100, 4: 0x07060504, 8: 0x0b0a0908, 12: 0x0f0e0d0c})
	wire := &packedTxnWire{inner: swdsim.New(target), limit: 54}
	dp := dap.NewDebugPort(swd.New(wire))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.remaining = 3
	target.err = errors.New("block read transport failed")
	buf := make([]byte, 16)
	for i := range buf {
		buf[i] = 0xee
	}
	n, err := mem.ReadBlock(t.Context(), 0, buf)
	if err == nil || !errors.Is(err, dap.ErrIndeterminate) || n != 8 {
		t.Fatalf("ReadBlock() after transport failure = %d, %v, want 8 and ErrIndeterminate", n, err)
	}
	wantPrefix := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	if !slices.Equal(buf[:n], wantPrefix) {
		t.Fatalf("ReadBlock() confirmed prefix = % x, want % x", buf[:n], wantPrefix)
	}
	for i, value := range buf[n:] {
		if value != 0xee {
			t.Fatalf("ReadBlock() changed unread byte %d to %#x", n+i, value)
		}
	}
	assertBlockedMEMAPUsesNoWire(t, mem, wire)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after transport failure: %v", err)
	}
}

func assertBlockedMEMAPUsesNoWire(t *testing.T, mem *dap.MemAP, wire *packedTxnWire) {
	t.Helper()
	traffic := len(wire.calls)
	if _, err := mem.ReadWord(t.Context(), 0); err == nil {
		t.Fatal("ReadWord() succeeded after block-read state became indeterminate")
	}
	if len(wire.calls) != traffic {
		t.Fatalf("blocked MEM-AP read added %d SWDIO calls", len(wire.calls)-traffic)
	}
}

func testMEMAPReadBlockWAIT(t *testing.T, simple bool, stage blockWAITStage) {
	target := newWaitTarget()
	var wireTarget swdsim.Target = target
	if simple {
		wireTarget = &simpleBlockTarget{waitTarget: target}
	}
	addMEMAP(t, target, 0, 0x00010001, nil)
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	if err := target.SetMEMAPBytes(apSel(0), 0, data); err != nil {
		t.Fatal(err)
	}
	dp := newDebugPort(t, wireTarget)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if target.OverrunDetectEnabled() == simple {
		t.Fatalf("ORUNDETECT = %t, want %t", target.OverrunDetectEnabled(), !simple)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	beforeCSWWrites := target.executed[apWrite(0x00)]
	target.arm(stage.waitFor, 101)
	buf := make([]byte, stage.length)
	n, err := mem.ReadBlock(t.Context(), stage.addr, buf)
	if err != nil || n != len(buf) {
		t.Fatalf("ReadBlock() after 101 WAITs = %d, %v; want %d, nil", n, err, len(buf))
	}
	want := data[stage.addr : stage.addr+uint64(stage.length)]
	if !slices.Equal(buf, want) {
		t.Fatalf("ReadBlock() = % x, want % x", buf, want)
	}
	if stage.waitFor == (dpRead(0x0c)) {
		if got := target.executed[apWrite(0x00)] - beforeCSWWrites; got != 2 {
			t.Fatalf("CSW writes after barrier WAIT = %d, want 2", got)
		}
	}
	for _, value := range target.abortValues {
		if value&1 != 0 {
			t.Fatalf("ABORT writes = %#v, want no DAPABORT while retrying the request", target.abortValues)
		}
	}
}

func TestMEMAPReadBlockWaitsUntilTargetResponds(t *testing.T) {
	stages := []blockWAITStage{
		{name: "CSW write", waitFor: apWrite(0x00), length: 4},
		{name: "CSW read", waitFor: apRead(0x00), length: 4},
		{name: "TAR write", waitFor: apWrite(0x04), length: 4},
		{name: "AP write barrier", waitFor: dpRead(0x0c), length: 4},
		{name: "word pipeline", waitFor: apRead(0x0c), length: 16},
		{name: "byte edge", waitFor: apRead(0x0c), addr: 1, length: 1},
	}
	grammars := []struct {
		name   string
		simple bool
	}{
		{name: "overrun"},
		{name: "simple", simple: true},
	}
	for _, grammar := range grammars {
		t.Run(grammar.name, func(t *testing.T) {
			for _, stage := range stages {
				t.Run(stage.name, func(t *testing.T) {
					testMEMAPReadBlockWAIT(t, grammar.simple, stage)
				})
			}
		})
	}
}

func TestMEMAPReadBlockRejectsWAITWithPendingSELECT(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 0x03020100})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DLCR); err != nil {
		t.Fatal(err)
	}
	target.deferWriteFor = dpWrite(0x08)
	target.deferWrite = true
	target.arm(dpRead(0x0c), 1)
	buf := []byte{0xee, 0xee, 0xee, 0xee}
	n, err := mem.ReadBlock(t.Context(), 0, buf)
	if !errors.Is(err, swd.ErrWait) || !errors.Is(err, dap.ErrIndeterminate) || n != 0 {
		t.Fatalf("ReadBlock() with ambiguous SELECT = %d, %v; want 0, WAIT, and ErrIndeterminate", n, err)
	}
	if !slices.Equal(buf, []byte{0xee, 0xee, 0xee, 0xee}) {
		t.Fatalf("ReadBlock() changed the destination after ambiguous SELECT: % x", buf)
	}
	traffic := len(target.requests)
	if _, err := mem.ReadWord(t.Context(), 0); err == nil {
		t.Fatal("ReadWord() succeeded after ambiguous SELECT")
	}
	if len(target.requests) != traffic {
		t.Fatalf("blocked MEM-AP read sent %d requests", len(target.requests)-traffic)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after ambiguous SELECT: %v", err)
	}
}

func TestMEMAPReadBlockStopsWaitingWhenContextEnds(t *testing.T) {
	target := &cancelingBlockWAITTarget{waitTarget: newWaitTarget(), after: 101}
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 0x03020100})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	target.cancel = cancel
	target.arm(apRead(0x0c), -1)
	buf := []byte{0xee, 0xee, 0xee, 0xee}
	n, err := mem.ReadBlock(ctx, 0, buf)
	if !errors.Is(err, context.Canceled) || errors.Is(err, swd.ErrWait) || n != 0 {
		t.Fatalf("ReadBlock() after cancellation = %d, %v; want 0, context cancellation, and no ErrWait", n, err)
	}
	if !slices.Equal(buf, []byte{0xee, 0xee, 0xee, 0xee}) {
		t.Fatalf("ReadBlock() changed the destination after cancellation: % x", buf)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after canceled block read: %v", err)
	}
}

func TestMEMAPReadBlockCleansUpWhenCancellationPreventsRetry(t *testing.T) {
	ctx := &stagedCancelContext{Context: t.Context(), done: make(chan struct{})}
	target := &stagedCancelBlockWAITTarget{simpleBlockTarget: &simpleBlockTarget{waitTarget: newWaitTarget()}, ctx: ctx}
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 0x03020100})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.arm(apRead(0x0c), -1)
	buf := []byte{0xee, 0xee, 0xee, 0xee}
	n, err := mem.ReadBlock(ctx, 0, buf)
	if !errors.Is(err, context.Canceled) || errors.Is(err, swd.ErrWait) || n != 0 {
		t.Fatalf("ReadBlock() when cancellation prevents retry = %d, %v; want 0, context cancellation, and no ErrWait", n, err)
	}
	if !slices.Equal(buf, []byte{0xee, 0xee, 0xee, 0xee}) {
		t.Fatalf("ReadBlock() changed the destination after cancellation: % x", buf)
	}
	assertDAPABORT(t, target.waitTarget)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after canceled retry: %v", err)
	}
}

func TestMEMAPReadBlockStopsAtConfiguredWAITLimit(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, nil)
	wire := &packedTxnWire{inner: swdsim.New(target), limit: 54}
	dp := dap.NewDebugPort(swd.New(wire), dap.WithMaxWaits(3))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	target.arm(apRead(0x0c), -1)
	buf := []byte{0xee, 0xee, 0xee, 0xee}
	n, err := mem.ReadBlock(t.Context(), 0, buf)
	if !errors.Is(err, swd.ErrWait) || n != 0 {
		t.Fatalf("ReadBlock() at WAIT limit = %d, %v, want 0 and WAIT", n, err)
	}
	if !slices.Equal(buf, []byte{0xee, 0xee, 0xee, 0xee}) {
		t.Fatalf("ReadBlock() changed unread bytes: % x", buf)
	}
	if target.attempts != 3 {
		t.Fatalf("DRW read attempts = %d, want 3", target.attempts)
	}
	assertDAPABORT(t, target)
	assertBlockedMEMAPUsesNoWire(t, mem, wire)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after WAIT limit: %v", err)
	}
}

func TestMEMAPReadBlockValidatesBeforeMemoryTraffic(t *testing.T) {
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
	buf := []byte{0xaa, 0xaa, 0xaa, 0xaa}
	beforeDRW := countRequests(target.requests, apRead(0x0c))
	if n, err := mem.ReadBlock(t.Context(), 1, buf); err == nil || n != 0 {
		t.Fatalf("ReadBlock() on a word-only MEM-AP = %d, %v; want 0, error", n, err)
	}
	if n, err := mem.ReadBlock(t.Context(), ^uint64(0), make([]byte, 2)); err == nil || n != 0 {
		t.Fatalf("overflowing ReadBlock() = %d, %v; want 0, error", n, err)
	}
	afterDRW := countRequests(target.requests, apRead(0x0c))
	if afterDRW != beforeDRW || !slices.Equal(buf, []byte{0xaa, 0xaa, 0xaa, 0xaa}) {
		t.Fatalf("validation changed memory-read traffic or destination: DRW %d to %d, buf % x", beforeDRW, afterDRW, buf)
	}
}

func TestEmptyMEMAPReadBlockUsesNoPort(t *testing.T) {
	var mem *dap.MemAP
	if n, err := mem.ReadBlock(t.Context(), 0, nil); err != nil || n != 0 {
		t.Fatalf("ReadBlock(nil) = %d, %v; want 0, nil", n, err)
	}
}
