package sim_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
	"github.com/jon/ostiole/target/cortexm"
	coresim "github.com/jon/ostiole/target/cortexm/sim"
)

func TestClearedManagedFaultPreservesSelectedAndPeerMEMAP(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[write], func(t *testing.T) {
			exerciseManagedScalarFault(t, write)
		})
	}
}

func TestClearedManagedBlockFaultPreservesMEMAP(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[write], func(t *testing.T) {
			exerciseManagedBlockFault(t, write)
		})
	}
}

func exerciseManagedBlockFault(t *testing.T, write bool) {
	t.Helper()
	device := &memoryDevice{err: dapsim.ErrBusFault, errAt: 0x108,
		data: map[uint64]byte{0x100: 0x11, 0x104: 0x22}}
	target := mappedTarget(t, 0x100, 16, device)
	addMEMAPFixture(t, target, 1, memAPIDR, map[uint32]uint32{0x200: 0x88776655})
	dp := enteredDAP(t, target)
	selected := acquireFaultMemory(t, dp, 0)
	peer := acquireFaultMemory(t, dp, 1)
	data := []byte{0x11, 0, 0, 0, 0x22, 0, 0, 0, 0x33, 0, 0, 0, 0x44, 0, 0, 0}
	var count int
	var err error
	if write {
		count, err = selected.WriteBlock(t.Context(), 0x100, data)
		if count != 0 || device.data[0x100] != 0x11 || device.data[0x104] != 0x22 || device.data[0x108] != 0 || device.data[0x10c] != 0 {
			t.Errorf("unconfirmed write prefix=%d,backing memory=%v", count, device.data)
		}
	} else {
		for i := range data {
			data[i] = 0xa5
		}
		count, err = selected.ReadBlock(t.Context(), 0x100, data)
		want := []byte{0x11, 0, 0, 0, 0x22, 0, 0, 0, 0xa5, 0xa5, 0xa5, 0xa5, 0xa5, 0xa5, 0xa5, 0xa5}
		if count != 8 || !slices.Equal(data, want) {
			t.Errorf("confirmed read prefix=%d,%x", count, data)
		}
	}
	if !errors.Is(err, dap.ErrFault) || len(device.accesses) != 3 {
		t.Fatalf("block fault count=%d,error=%v,accesses=%v", count, err, device.accesses)
	}
	if errors.Is(err, dap.ErrIndeterminate) != write {
		t.Errorf("block write=%t,uncertainty=%v", write, err)
	}
	device.err = nil
	if got, err := selected.ReadScalar(t.Context(), 0x100, dap.Size32); err != nil || got != 0x11 {
		t.Errorf("selected after block FAULT=%#x,%v", got, err)
	}
	if got, err := peer.ReadScalar(t.Context(), 0x200, dap.Size32); err != nil || got != 0x88776655 {
		t.Errorf("peer after block FAULT=%#x,%v", got, err)
	}
}

func TestClearedMemoryFaultAllowsBorrowingTargetRelease(t *testing.T) {
	for _, faultPeer := range []bool{false, true} {
		t.Run(map[bool]string{false: "same AP", true: "peer AP"}[faultPeer], func(t *testing.T) {
			exerciseFaultTargetRelease(t, faultPeer)
		})
	}
}

func exerciseFaultTargetRelease(t *testing.T, faultPeer bool) {
	t.Helper()
	device := &memoryDevice{err: dapsim.ErrBusFault}
	target := dapsim.New(0x2ba01477)
	for index := range uint8(2) {
		addMEMAPFixture(t, target, index, memAPIDR, nil)
	}
	core, err := coresim.New(coresim.Config{Profile: coresim.M0})
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []uint64{0xe000ed00, 0xe000ed30, 0xe000edf0} {
		if err := target.MapMEMAPDevice(apSel(0), addr, 4, core); err != nil {
			t.Fatal(err)
		}
	}
	index := uint8(0)
	if faultPeer {
		index = 1
	}
	if err := target.MapMEMAPDevice(apSel(index), 0x100, 4, device); err != nil {
		t.Fatal(err)
	}
	dp := enteredDAP(t, target)
	selected := acquireFaultMemory(t, dp, 0)
	peer := acquireFaultMemory(t, dp, 1)
	cpu, err := cortexm.Acquire(t.Context(), selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := cpu.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	if core.Snapshot().DHCSR&(1<<17) == 0 {
		t.Fatal("fixture core did not halt")
	}
	faultMemory := selected
	if faultPeer {
		faultMemory = peer
	}
	if _, err := faultMemory.ReadScalar(t.Context(), 0x100, dap.Size32); !errors.Is(err, dap.ErrFault) {
		t.Fatalf("memory FAULT=%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cpu.Release(ctx); err != nil {
		t.Errorf("borrowing target release after cleared FAULT: %v", err)
	}
	if got := core.Snapshot().DHCSR; got&(1|2|1<<17) != 0 {
		t.Errorf("target still enabled or halted: DHCSR=%#x", got)
	}
}

func exerciseManagedScalarFault(t *testing.T, write bool) {
	t.Helper()
	device := &memoryDevice{err: dapsim.ErrBusFault}
	target := mappedTarget(t, 0x100, 4, device)
	addMEMAPFixture(t, target, 1, memAPIDR, map[uint32]uint32{0x200: 0x88776655})
	dp := enteredDAP(t, target)
	selected := acquireFaultMemory(t, dp, 0)
	peer := acquireFaultMemory(t, dp, 1)
	var err error
	if write {
		err = selected.WriteScalar(t.Context(), 0x100, dap.Size32, 0x44332211)
	} else {
		_, err = selected.ReadScalar(t.Context(), 0x100, dap.Size32)
	}
	if !errors.Is(err, dap.ErrFault) {
		t.Fatalf("managed memory failure=%v, want FAULT", err)
	}
	if errors.Is(err, dap.ErrIndeterminate) != write {
		t.Errorf("scalar write=%t,uncertainty=%v", write, err)
	}
	if len(device.accesses) != 1 {
		t.Fatalf("faulting access replayed: %v", device.accesses)
	}
	device.err = nil
	device.data = map[uint64]byte{0x100: 0x11, 0x101: 0x22, 0x102: 0x33, 0x103: 0x44}
	if got, err := selected.ReadScalar(t.Context(), 0x100, dap.Size32); err != nil || got != 0x44332211 {
		t.Errorf("selected AP after cleared FAULT=%#x,%v", got, err)
	}
	if got, err := peer.ReadScalar(t.Context(), 0x200, dap.Size32); err != nil || got != 0x88776655 {
		t.Errorf("peer AP after cleared FAULT=%#x,%v", got, err)
	}
	if len(device.accesses) != 2 {
		t.Errorf("accepted device accesses=%d, want fault and explicit retry", len(device.accesses))
	}
}

func acquireFaultMemory(t *testing.T, dp *dap.DebugPort, index uint8) *dap.MemAP {
	t.Helper()
	mem, err := dap.OpenMemAP(t.Context(), dp, apSel(index))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mem.Release(ctx); err != nil {
			t.Errorf("release AP%d: %v", index, err)
		}
	})
	return mem
}

func TestUnsafeAPEffectsStillInvalidateManagedClients(t *testing.T) {
	for _, mode := range []string{"raw AP fault", "DAPABORT", "accepted write failure"} {
		t.Run(mode, func(t *testing.T) { exerciseUnsafeAPEffect(t, mode) })
	}
}

func exerciseUnsafeAPEffect(t *testing.T, mode string) {
	t.Helper()
	device := new(memoryDevice)
	target := mappedTarget(t, 0x100, 4, device)
	addMEMAPFixture(t, target, 1, memAPIDR, nil)
	dp := enteredDAP(t, target)
	selected := acquireFaultMemory(t, dp, 0)
	peer := acquireFaultMemory(t, dp, 1)
	if _, err := selected.ReadScalar(t.Context(), 0x100, dap.Size32); err != nil {
		t.Fatal(err)
	}
	var err error
	switch mode {
	case "raw AP fault":
		device.err = dapsim.ErrBusFault
		_, err = dp.ReadRawAP(t.Context(), apSel(0).Address(12))
		if !errors.Is(err, dap.ErrFault) {
			t.Fatalf("raw fault=%v", err)
		}
	case "DAPABORT":
		err = dp.WriteDP(t.Context(), dap.ABORT, 1)
		if err != nil {
			t.Fatal(err)
		}
	case "accepted write failure":
		device.err = errors.New("device lost after accepting write")
		device.afterWrite = true
		err = selected.WriteScalar(t.Context(), 0x100, dap.Size32, 0x44332211)
		if !errors.Is(err, dap.ErrIndeterminate) {
			t.Fatalf("accepted failure=%v, want uncertainty", err)
		}
	}
	device.err = nil
	count := len(device.accesses)
	for _, mem := range []*dap.MemAP{selected, peer} {
		if _, err := mem.ReadScalar(t.Context(), 0x100, dap.Size32); err == nil {
			t.Error("unsafe AP effect left stale client usable")
		}
	}
	if len(device.accesses) != count {
		t.Error("invalidated client performed target traffic")
	}
}

type failedFaultCleanup struct {
	*dapsim.Target
	clearError error
}

func (t *failedFaultCleanup) Acknowledge(ctx context.Context, req swdsim.Request) error {
	if t.clearError != nil && !req.AP && !req.Read && req.Addr == 0 {
		return t.clearError
	}
	return t.Target.Acknowledge(ctx, req)
}

func TestCapturedManagedFaultWithFailedCleanupStillInvalidates(t *testing.T) {
	device := &memoryDevice{err: dapsim.ErrBusFault}
	target := &failedFaultCleanup{Target: mappedTarget(t, 0x100, 4, device)}
	addMEMAPFixture(t, target.Target, 1, memAPIDR, nil)
	dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dp.Release(ctx); err != nil {
			t.Error(err)
		}
	})
	selected := acquireFaultMemory(t, dp, 0)
	peer := acquireFaultMemory(t, dp, 1)
	cleanupError := errors.New("sticky clear transport failed")
	target.clearError = cleanupError
	_, err := selected.ReadScalar(t.Context(), 0x100, dap.Size32)
	var fault *dap.FaultError
	if !errors.Is(err, dap.ErrFault) || !errors.Is(err, cleanupError) || !errors.Is(err, dap.ErrIndeterminate) || !errors.As(err, &fault) || !fault.StateValid {
		t.Fatalf("captured fault and cleanup error=%v", err)
	}
	target.clearError = nil
	device.err = nil
	count := len(device.accesses)
	for _, mem := range []*dap.MemAP{selected, peer} {
		if _, err := mem.ReadScalar(t.Context(), 0x100, dap.Size32); err == nil {
			t.Error("failed cleanup left client usable")
		}
	}
	if len(device.accesses) != count {
		t.Error("client performed traffic after failed cleanup")
	}
}
