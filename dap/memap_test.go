package dap_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestMEMAPReadsSizedValuesInBothByteOrders(t *testing.T) {
	tests := []struct {
		name string
		cfg  uint32
		want [4]uint64
	}{
		{name: "little endian", want: [4]uint64{0x22, 0x4433, 0x44332211, 0x8877665544332211}},
		{name: "big endian", cfg: 1, want: [4]uint64{0x22, 0x3344, 0x11223344, 0x1122334455667788}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := sim.New(0x2ba01477)
			addMEMAP(t, target, 0, 0x00010001, nil)
			if err := target.SetMEMAPCFG(apSel(0), test.cfg|1<<2); err != nil {
				t.Fatal(err)
			}
			if err := target.SetMEMAPBytes(apSel(0), 0x100, []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}); err != nil {
				t.Fatal(err)
			}
			mem, err := dap.OpenMemAP(t.Context(), enteredDAPClient(t, target), apSel(0))
			if err != nil {
				t.Fatal(err)
			}
			accesses := []struct {
				addr uint64
				size dap.TransferSize
			}{
				{addr: 0x101, size: dap.Size8},
				{addr: 0x102, size: dap.Size16},
				{addr: 0x100, size: dap.Size32},
				{addr: 0x100, size: dap.Size64},
			}
			for i, access := range accesses {
				got, err := mem.ReadScalar(t.Context(), access.addr, access.size)
				if err != nil {
					t.Fatal(err)
				}
				if got != test.want[i] {
					t.Fatalf("ReadScalar(%#x, %d) = %#x, want %#x", access.addr, access.size, got, test.want[i])
				}
			}
		})
	}
}

func TestMEMAPWritesSizedValuesWithoutChangingNeighboringLanes(t *testing.T) {
	tests := []struct {
		name string
		cfg  uint32
		want []byte
	}{
		{name: "little endian", want: []byte{0xaa, 0x12, 0x56, 0x34, 0x44, 0x33, 0x22, 0x11}},
		{name: "big endian", cfg: 1, want: []byte{0xaa, 0x12, 0x34, 0x56, 0x11, 0x22, 0x33, 0x44}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := sim.New(0x2ba01477)
			addMEMAP(t, target, 0, 0x00010001, nil)
			if err := target.SetMEMAPCFG(apSel(0), test.cfg); err != nil {
				t.Fatal(err)
			}
			if err := target.SetMEMAPBytes(apSel(0), 0x100, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x99, 0x88}); err != nil {
				t.Fatal(err)
			}
			mem, err := dap.OpenMemAP(t.Context(), enteredDAPClient(t, target), apSel(0))
			if err != nil {
				t.Fatal(err)
			}
			if err := mem.WriteScalar(t.Context(), 0x101, dap.Size8, 0x12); err != nil {
				t.Fatal(err)
			}
			if err := mem.WriteScalar(t.Context(), 0x102, dap.Size16, 0x3456); err != nil {
				t.Fatal(err)
			}
			if err := mem.WriteScalar(t.Context(), 0x104, dap.Size32, 0x11223344); err != nil {
				t.Fatal(err)
			}
			got, err := target.MEMAPBytes(apSel(0), 0x100, 8)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("target bytes = % x, want % x", got, test.want)
			}
		})
	}
}

func TestMEMAPSize64RequiresLargeDataExtension(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, nil)
	mem, err := dap.OpenMemAP(t.Context(), enteredDAPClient(t, target), apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadScalar(t.Context(), 0, dap.Size64); err == nil {
		t.Fatal("ReadScalar(Size64) succeeded without CFG.LD")
	}
	if err := mem.WriteScalar(t.Context(), 0, dap.Size64, 0); err == nil {
		t.Fatal("WriteScalar(Size64) succeeded without CFG.LD")
	}
}

func TestMEMAPRejectsUnsupportedSizeBeforeMemoryTraffic(t *testing.T) {
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
	if err := mem.WriteScalar(t.Context(), 1, dap.Size8, 0xff); err == nil {
		t.Fatal("WriteScalar(Size8) succeeded on a word-only MEM-AP")
	}
	afterDRW := countRequests(target.requests, apWrite(0x0c))
	if afterDRW != beforeDRW {
		t.Fatalf("DRW writes after rejected size = %d, want %d", afterDRW, beforeDRW)
	}
}

func countRequests(requests []swdsim.Request, want swdsim.Request) int {
	count := 0
	for _, req := range requests {
		if req == want {
			count++
		}
	}
	return count
}

type cancelAPTarget struct {
	*waitTarget
	cancel       context.CancelFunc
	req          swdsim.Request
	afterBarrier bool
	started      bool
}

func (t *cancelAPTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	value, err := t.waitTarget.Read(ctx, req)
	t.cancelAfter(req, err)
	return value, err
}

func (t *cancelAPTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	err := t.waitTarget.Write(ctx, req, value)
	t.cancelAfter(req, err)
	return err
}

func (t *cancelAPTarget) cancelAfter(req swdsim.Request, err error) {
	if err != nil || t.cancel == nil {
		return
	}
	if req == t.req && t.afterBarrier {
		t.started = true
		return
	}
	if req == t.req || t.started && req == dpRead(0x0c) {
		t.cancel()
		t.cancel = nil
	}
}

func TestMEMAPWritesSize64InBothByteOrders(t *testing.T) {
	tests := []struct {
		name string
		cfg  uint32
		want []byte
	}{
		{name: "little endian", cfg: 1 << 2, want: []byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}},
		{name: "big endian", cfg: 1<<2 | 1, want: []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}},
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
			if err := mem.WriteScalar(t.Context(), 0x100, dap.Size64, 0x1122334455667788); err != nil {
				t.Fatal(err)
			}
			got, err := target.MEMAPBytes(apSel(0), 0x100, 8)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("target bytes = % x, want % x", got, test.want)
			}
		})
	}
}

type interruptedSize64Case struct {
	name              string
	req               swdsim.Request
	afterBarrier      bool
	wantIndeterminate bool
	run               func(context.Context, *dap.MemAP) error
}

func TestMEMAPReleaseTerminatesInterruptedSize64TransferBeforeRestoringAddress(t *testing.T) {
	tests := []interruptedSize64Case{
		{name: "read before barrier", req: apRead(0x0c), wantIndeterminate: true, run: func(ctx context.Context, mem *dap.MemAP) error {
			_, err := mem.ReadScalar(ctx, 0x100, dap.Size64)
			return err
		}},
		{name: "read after low word", req: apRead(0x0c), afterBarrier: true, run: func(ctx context.Context, mem *dap.MemAP) error {
			_, err := mem.ReadScalar(ctx, 0x100, dap.Size64)
			return err
		}},
		{name: "write before barrier", req: apWrite(0x0c), wantIndeterminate: true, run: func(ctx context.Context, mem *dap.MemAP) error {
			return mem.WriteScalar(ctx, 0x100, dap.Size64, 0x1122334455667788)
		}},
		{name: "write after low word", req: apWrite(0x0c), afterBarrier: true, run: func(ctx context.Context, mem *dap.MemAP) error {
			return mem.WriteScalar(ctx, 0x100, dap.Size64, 0x1122334455667788)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { testInterruptedSize64Cleanup(t, test) })
	}
}

func testInterruptedSize64Cleanup(t *testing.T, test interruptedSize64Case) {
	t.Helper()
	target := &cancelAPTarget{waitTarget: newWaitTarget(), req: test.req, afterBarrier: test.afterBarrier}
	addMEMAP(t, target, 0, 0x00010001, nil)
	if err := target.SetMEMAPCFG(apSel(0), 1<<2); err != nil {
		t.Fatal(err)
	}
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
	err = test.run(ctx, mem)
	if !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrIndeterminate) != test.wantIndeterminate {
		t.Fatalf("scalar %s error = %v, want context cancellation with indeterminate=%t", test.name, err, test.wantIndeterminate)
	}
	traffic := len(wire.calls)
	assertMEMAPHandleInvalid(t, mem)
	if len(wire.calls) != traffic {
		t.Fatalf("blocked MEM-AP read added %d SWDIO calls", len(wire.calls)-traffic)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil {
		t.Fatal("ReadDP() succeeded before interrupted Size64 cleanup")
	}
	if len(wire.calls) != traffic {
		t.Fatalf("blocked debug-port read added %d SWDIO calls", len(wire.calls)-traffic)
	}
	before := len(target.requests)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after interrupted Size64 %s: %v", test.name, err)
	}
	assertFirstReleaseAPRequestIsCSW(t, target.requests[before:])
	if err := dp.Release(t.Context()); err != nil {
		t.Fatalf("DebugPort.Release() after interrupted Size64 %s: %v", test.name, err)
	}
}

func assertFirstReleaseAPRequestIsCSW(t *testing.T, requests []swdsim.Request) {
	t.Helper()
	for _, req := range requests {
		if !req.AP {
			continue
		}
		if req != apWrite(0x00) {
			t.Fatalf("first AP request during Release = %+v, want CSW write", req)
		}
		return
	}
	t.Fatal("Release() sent no AP request")
}

func TestMEMAPUsesAndRestoresLargeTargetAddress(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, nil)
	if err := target.SetMEMAPCFG(apSel(0), 1<<1); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPBytes(apSel(0), 0x100000000, []byte{0x78, 0x56, 0x34, 0x12}); err != nil {
		t.Fatal(err)
	}
	dp := enteredDAPClient(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x08), 0xdeadbeef); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	value, err := mem.ReadScalar(t.Context(), 0x100000000, dap.Size32)
	if err != nil || value != 0x12345678 {
		t.Fatalf("ReadScalar() = %#x, %v; want 0x12345678, nil", value, err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAPRegister(t, dp, 0x08, 0xdeadbeef)
}

func TestMEMAPScalarValidationSendsNoTraffic(t *testing.T) {
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
	before := len(target.requests)
	_, readErr := mem.ReadScalar(t.Context(), 1, dap.Size16)
	writeErr := mem.WriteScalar(t.Context(), 1<<32, dap.Size32, 0)
	_, largeErr := mem.ReadScalar(t.Context(), 0, dap.Size64)
	oversizedErr := mem.WriteScalar(t.Context(), 0, dap.Size8, 0x100)
	if readErr == nil || writeErr == nil || largeErr == nil || oversizedErr == nil {
		t.Fatalf("validation errors: read=%v write=%v size64=%v oversized=%v", readErr, writeErr, largeErr, oversizedErr)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("requests after validation errors = %d, want %d", got, before)
	}
}

func TestMEMAPWriteEndsWithCompletionBarrier(t *testing.T) {
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
	if err := mem.WriteScalar(t.Context(), 0, dap.Size32, 1); err != nil {
		t.Fatal(err)
	}
	if got := target.requests[len(target.requests)-1]; got != dpRead(0x0c) {
		t.Fatalf("last request = %+v, want RDBUFF", got)
	}
}

func TestIndeterminateMEMAPWriteInvalidatesOperationalState(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	err = mem.WriteScalar(t.Context(), 0, dap.Size32, 2)
	if !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("WriteScalar() error = %v, want %v and indeterminate", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
	target.failBarrier = false
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("Release() after indeterminate write: %v", err)
	}
}

func TestReadAlignedMEMAPWord(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{
		0xe000ed00: 0x410cc200,
	})
	dp := enteredDAPClient(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 0xa5000051); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	value, err := mem.ReadWord(t.Context(), 0xe000ed00)
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x410cc200 {
		t.Fatalf("ReadWord() = %#08x, want 0x410cc200", value)
	}
	csw, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0))
	if err != nil {
		t.Fatal(err)
	}
	if csw != 0xa5000042 {
		t.Fatalf("CSW = %#08x, want preserved value 0xa5000042", csw)
	}
}

func TestMEMAPReleaseRestoresRegisterState(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{
		0xe000ed00: 0x410cc200,
	})
	dp := enteredDAPClient(t, target)
	const (
		originalCSW = uint32(0xa5000051)
		originalTAR = uint32(0x20000000)
	)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), originalCSW); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x04), originalTAR); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0xe000ed00); err != nil {
		t.Fatal(err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAPRegister(t, dp, 0x00, originalCSW)
	assertAPRegister(t, dp, 0x04, originalTAR)
}

func TestMEMAPReleaseCanRetry(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := mem.Release(canceled); err == nil {
		t.Fatal("Release() succeeded with a canceled context")
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("retry Release(): %v", err)
	}
}

func TestMEMAPRejectsUnalignedWord(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, nil)
	mem, err := dap.OpenMemAP(t.Context(), enteredDAPClient(t, target), apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 3); err == nil {
		t.Fatal("ReadWord() succeeded with an unaligned address")
	}
}

func TestImmediateRawAPWriteInvalidatesMEMAP(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); err != nil {
		t.Fatal(err)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestQueuedRawAPWriteInvalidatesMEMAP(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	write := txn.WriteRawAP(apSel(0).Address(0), 2)
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := write.Err(); err != nil {
		t.Fatal(err)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestImmediateRawAPReadInvalidatesMEMAP(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 2); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x0c)); err != nil {
		t.Fatal(err)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestQueuedRawAPReadInvalidatesMEMAP(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x00), 2); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	read := txn.ReadRawAP(apSel(0).Address(0x0c))
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := read.Value(); err != nil {
		t.Fatal(err)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestAPIDRReadsKeepMEMAPUsable(t *testing.T) {
	target := sim.New(0x2ba01477)
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := enteredDAPClient(t, target)
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	read := txn.ReadAPIDR(apSel(0))
	if err := txn.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := read.Value(); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after APIDR reads: %v", err)
	}
}

func TestIndeterminateImmediateRawAPWriteInvalidatesMEMAP(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); !errors.Is(err, errBarrier) {
		t.Fatalf("WriteRawAP() error = %v, want %v", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestIndeterminateQueuedRawAPWriteInvalidatesMEMAP(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	txn := dp.NewTxn()
	write := txn.WriteRawAP(apSel(0).Address(0), 2)
	if err := txn.Commit(t.Context()); !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want %v and indeterminate", err, errBarrier)
	}
	if err := write.Err(); !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("write result error = %v, want %v and indeterminate", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestIndeterminateImmediateRawAPReadInvalidatesMEMAP(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x00)); !errors.Is(err, errBarrier) {
		t.Fatalf("ReadRawAP() error = %v, want %v", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestIndeterminateQueuedRawAPReadInvalidatesMEMAP(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	target.failBarrier = true
	txn := dp.NewTxn()
	read := txn.ReadRawAP(apSel(0).Address(0x00))
	if err := txn.Commit(t.Context()); !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("Commit() error = %v, want %v and indeterminate", err, errBarrier)
	}
	if _, err := read.Value(); !errors.Is(err, errBarrier) || !errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("read result error = %v, want %v and indeterminate", err, errBarrier)
	}
	assertMEMAPHandleInvalid(t, mem)
}

func TestInvalidRawAPReadKeepsMEMAPUsable(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x02)); err == nil {
		t.Fatal("ReadRawAP() accepted unaligned address 0x02")
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after invalid raw read: %v", err)
	}
}

func TestCanceledRawAPReadKeepsMEMAPUsableWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(target.requests)
	if _, err := dp.ReadRawAP(ctx, apSel(0).Address(0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadRawAP() error = %v, want context canceled", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("canceled raw read sent %d requests", got-before)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after canceled raw read: %v", err)
	}
}

func TestCanceledQueuedRawAPReadKeepsMEMAPUsableWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	txn := dp.NewTxn()
	read := txn.ReadRawAP(apSel(0).Address(0))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(target.requests)
	if err := txn.Commit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit() error = %v, want context canceled", err)
	}
	if _, err := read.Value(); !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("read result error = %v, want determinate context cancellation", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("canceled queued raw read sent %d requests", got-before)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after canceled queued raw read: %v", err)
	}
}

func TestRejectedRawAPWritesKeepMEMAPUsable(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x02), 2); err == nil {
		t.Fatal("WriteRawAP() accepted unaligned address 0x02")
	}
	target.armFault(apWrite(0x00))
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("WriteRawAP() error = %v, want %v", err, swd.ErrFault)
	}
	value, err := mem.ReadWord(t.Context(), 0)
	if err != nil {
		t.Fatalf("ReadWord() after rejected raw writes: %v", err)
	}
	if value != 1 {
		t.Fatalf("ReadWord() = %#08x, want 1", value)
	}
	target.dropWriteFor = apWrite(0x00)
	target.dropWrite = true
	target.stickyOnDrop = testWriteDataError
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); !errors.Is(err, swd.ErrFault) {
		t.Fatalf("WriteRawAP() after abandoned data error = %v, want %v", err, swd.ErrFault)
	}
	if _, err := mem.ReadWord(t.Context(), 0); err != nil {
		t.Fatalf("ReadWord() after WDATAERR: %v", err)
	}
}

func TestCanceledRawAPWriteKeepsMEMAPUsableWithoutTraffic(t *testing.T) {
	target := newWaitTarget()
	addMEMAP(t, target, 0, 0x00010001, map[uint32]uint32{0: 1})
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(target.requests)
	if err := dp.WriteRawAP(ctx, apSel(0).Address(0), 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteRawAP() error = %v, want context canceled", err)
	}
	if got := len(target.requests); got != before {
		t.Fatalf("canceled raw write sent %d requests", got-before)
	}
	value, err := mem.ReadWord(t.Context(), 0)
	if err != nil {
		t.Fatalf("ReadWord() after canceled raw write: %v", err)
	}
	if value != 1 {
		t.Fatalf("ReadWord() = %#08x, want 1", value)
	}
}

func TestOpenMEMAPRejectsAbsentAndNonMemoryPorts(t *testing.T) {
	target := sim.New(0x2ba01477)
	addAP(t, target, 1, 0x00000001)
	dp := enteredDAPClient(t, target)
	var zero dap.APSel
	if _, err := dap.OpenMemAP(t.Context(), dp, zero); err == nil {
		t.Fatal("OpenMemAP() accepted a zero APSel")
	}
	if _, err := dap.OpenMemAP(t.Context(), dp, apSel(0)); err == nil {
		t.Fatal("OpenMemAP() accepted an absent AP")
	}
	if _, err := dap.OpenMemAP(t.Context(), dp, apSel(1)); err == nil {
		t.Fatal("OpenMemAP() accepted a non-MEM AP")
	}
}

func TestNilMEMAPClient(t *testing.T) {
	if _, err := dap.OpenMemAP(t.Context(), nil, apSel(0)); err == nil {
		t.Fatal("OpenMemAP() succeeded without a debug port")
	}
	var mem *dap.MemAP
	if _, err := mem.ReadWord(t.Context(), 0); err == nil {
		t.Fatal("ReadWord() succeeded on a nil MEM-AP")
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatalf("nil Release(): %v", err)
	}
}

func assertAPRegister(t *testing.T, dp *dap.DebugPort, address uint8, want uint32) {
	t.Helper()
	got, err := dp.ReadRawAP(t.Context(), apSel(0).Address(address))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("AP register %#02x = %#08x, want %#08x", address, got, want)
	}
}

func assertMEMAPHandleInvalid(t *testing.T, mem *dap.MemAP) {
	t.Helper()
	if _, err := mem.ReadWord(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("ReadWord() error = %v, want invalidated MEM-AP", err)
	}
}

func enteredDAPClient(t *testing.T, target swdsim.Target) *dap.DebugPort {
	t.Helper()
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})
	return dp
}
