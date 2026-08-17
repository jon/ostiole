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

func (t *failingBlockReadTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	if t.err != nil && req == apRead(0x0c) {
		if t.remaining == 0 {
			return 0, t.err
		}
		t.remaining--
	}
	return t.waitTarget.Read(ctx, req)
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
	target.armFaultAfter(apRead(0x0c), 3)
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

func TestMEMAPReadBlockRetriesTheWAITedPipelineRequest(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 0x03020100, 4: 0x07060504, 8: 0x0b0a0908, 12: 0x0f0e0d0c})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.arm(apRead(0x0c), 1)
	buf := make([]byte, 16)
	n, err := mem.ReadBlock(t.Context(), 0, buf)
	if err != nil || n != len(buf) {
		t.Fatalf("ReadBlock() after WAIT = %d, %v", n, err)
	}
	for i, value := range buf {
		if value != byte(i) {
			t.Fatalf("ReadBlock() byte %d = %#x", i, value)
		}
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
		t.Fatalf("ReadBlock() = %d, %v on a word-only MEM-AP", n, err)
	}
	if n, err := mem.ReadBlock(t.Context(), ^uint64(0), make([]byte, 2)); err == nil || n != 0 {
		t.Fatalf("overflowing ReadBlock() = %d, %v", n, err)
	}
	afterDRW := countRequests(target.requests, apRead(0x0c))
	if afterDRW != beforeDRW || !slices.Equal(buf, []byte{0xaa, 0xaa, 0xaa, 0xaa}) {
		t.Fatalf("validation changed memory-read traffic or destination: DRW %d to %d, buf % x", beforeDRW, afterDRW, buf)
	}
}

func TestEmptyMEMAPReadBlockUsesNoPort(t *testing.T) {
	var mem *dap.MemAP
	if n, err := mem.ReadBlock(t.Context(), 0, nil); err != nil || n != 0 {
		t.Fatalf("ReadBlock(nil) = %d, %v", n, err)
	}
}
