package dap_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
)

func TestRegisterBindingDoesNotRepostCompletedAPReads(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if model.connects != 0 || len(model.requests) != 0 {
		t.Fatal("constructor sent traffic")
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	id, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
	if err != nil || id.Raw != 0x24770011 {
		t.Fatalf("completed APIDR=%#x,%v", id.Raw, err)
	}
	for _, req := range model.requests {
		if req == dpRead(12) {
			t.Fatal("owner drained a completed register operation again")
		}
	}
}

func TestRegisterMEMAPMemoryAndRestoration(t *testing.T) {
	model := newRegisterModel(t)
	model.target.SetOverrunDetect(true)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	sel := dap.NewAPSel(1)
	if err := dp.WriteRawAP(t.Context(), sel.Address(4), 0x88); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mem.Release(ctx); err != nil {
			t.Error(err)
		}
	})
	if value, err := mem.ReadWord(t.Context(), 0x100); err != nil || value != 42 {
		t.Fatalf("scalar=%d,%v", value, err)
	}
	if n, err := mem.WriteBlock(t.Context(), 0x104, []byte{1, 2, 3, 4}); err != nil || n != 4 {
		t.Fatalf("block write=%d,%v", n, err)
	}
	data, err := model.target.MEMAPBytes(sel, 0x104, 4)
	if err != nil || !bytes.Equal(data, []byte{1, 2, 3, 4}) {
		t.Fatalf("backing bytes=%v,%v", data, err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if tar, err := dp.ReadRawAP(t.Context(), sel.Address(4)); err != nil || tar != 0x88 {
		t.Fatalf("restored TAR=%#x,%v", tar, err)
	}
	requireRegisterReleased(t, model, dp)
}

func TestRegisterRejectedWAITAndUncertainOperationsDiffer(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model), dap.WithMaxWaits(2))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			model.failFor = apRead(12)
			if uncertain {
				model.failStatus = dap.RegisterIndeterminate
				model.failCause = dap.ErrWait
			} else {
				model.waits = 1
			}
			id, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
			attempts := 0
			for _, req := range model.requests {
				if req == apRead(12) {
					attempts++
				}
			}
			if uncertain {
				if !errors.Is(err, dap.ErrIndeterminate) || attempts != 1 {
					t.Fatalf("uncertain=%v attempts=%d", err, attempts)
				}
			} else if err != nil || id.Raw != 0x24770011 || attempts != 2 {
				t.Fatalf("rejected retry=%#x,%v attempts=%d", id.Raw, err, attempts)
			}
		})
	}
}

func requireRegisterReleased(t *testing.T, model *registerModel, dp *dap.DebugPort) {
	t.Helper()
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctrl, err := model.target.Read(t.Context(), dpRead(4))
	if err != nil || ctrl != 1 {
		t.Fatalf("restored CTRL/STAT=%#x,%v", ctrl, err)
	}
	before := len(model.requests)
	if err := dp.Release(t.Context()); err != nil || len(model.requests) != before || model.releases != 1 {
		t.Fatal("completed release repeated")
	}
}

type registerBusDevice struct {
	fail     bool
	accesses int
}

func (d *registerBusDevice) Read(_ context.Context, _ uint64, data []byte) error {
	d.accesses++
	if d.fail {
		return dapsim.ErrBusFault
	}
	for i := range data {
		data[i] = 42
	}
	return nil
}
func (d *registerBusDevice) Write(_ context.Context, _ uint64, _ []byte) error {
	d.accesses++
	if d.fail {
		return dapsim.ErrBusFault
	}
	return nil
}

func TestRegisterManagedFaultRetainsClients(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) { exerciseRegisterManagedFault(t, write) })
	}
}

func exerciseRegisterManagedFault(t *testing.T, write bool) {
	t.Helper()
	model := newRegisterModel(t)
	device := &registerBusDevice{}
	sel := dap.NewAPSel(1)
	peerSel := dap.NewAPSel(2)
	if err := model.target.AddMEMAP(peerSel, 0x24770011, map[uint32]uint32{0x100: 7}); err != nil {
		t.Fatal(err)
	}
	if err := model.target.MapMEMAPDevice(sel, 0x100, 4, device); err != nil {
		t.Fatal(err)
	}
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := dap.OpenMemAP(t.Context(), dp, peerSel)
	if err != nil {
		t.Fatal(err)
	}
	device.fail = true
	if write {
		err = mem.WriteScalar(t.Context(), 0x100, dap.Size32, 42)
	} else {
		_, err = mem.ReadWord(t.Context(), 0x100)
	}
	if !errors.Is(err, dap.ErrFault) || errors.Is(err, dap.ErrIndeterminate) != write || device.accesses != 1 {
		t.Fatalf("fault=%v effects=%d", err, device.accesses)
	}
	device.fail = false
	if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
		t.Errorf("faulting client unusable: %v", err)
	}
	if value, err := peer.ReadWord(t.Context(), 0x100); err != nil || value != 7 {
		t.Errorf("peer=%d,%v", value, err)
	}
	if err := peer.Release(t.Context()); err != nil {
		t.Error(err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Error(err)
	}
}

func TestRegisterCompletedWriteErrorKeepsBlockPrefix(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	mem, err := dap.OpenMemAP(t.Context(), dp, dap.NewAPSel(1))
	if err != nil {
		t.Fatal(err)
	}
	model.failFor = apWrite(12)
	model.failStatus = dap.RegisterCompleted
	model.failCause = context.Canceled
	model.afterWrite = true
	n, err := mem.WriteBlock(t.Context(), 0x100, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	if n != 4 || !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrIndeterminate) {
		t.Fatalf("completed write prefix=%d,%v", n, err)
	}
	data, err := model.target.MEMAPBytes(dap.NewAPSel(1), 0x100, 8)
	if err != nil || !bytes.Equal(data, []byte{1, 2, 3, 4, 43, 0, 0, 0}) {
		t.Fatalf("backing=%v,%v", data, err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterCompletedReadCancellationKeepsPrefix(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	mem, err := dap.OpenMemAP(t.Context(), dp, dap.NewAPSel(1))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	model.failFor = apRead(12)
	model.failStatus = dap.RegisterCompleted
	model.failCause = context.Canceled
	model.afterRead = true
	model.onCompletion = cancel
	data := make([]byte, 8)
	n, err := mem.ReadBlock(ctx, 0x100, data)
	if n != 4 || !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrIndeterminate) || !bytes.Equal(data, []byte{42, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("completed read=%d,%v,%v", n, err, data)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterInvalidOutcomeRequiresRepair(t *testing.T) {
	for _, status := range []dap.RegisterStatus{0, dap.RegisterCompleted, dap.RegisterUnsent} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			model.failFor = apRead(12)
			model.failStatus = status
			model.failCause = dap.ErrIndeterminate
			if status == dap.RegisterCompleted {
				model.failCause = dap.ErrNotExecuted
			}
			_, err := dp.ReadAPIDR(t.Context(), dap.NewAPSel(1))
			if !errors.Is(err, dap.ErrProtocol) || !errors.Is(err, dap.ErrIndeterminate) {
				t.Fatalf("invalid outcome=%v", err)
			}
			before := len(model.requests)
			if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || len(model.requests) != before {
				t.Fatal("traffic after invalid transport outcome")
			}
		})
	}
}

type registerBlockDevice struct {
	registerBusDevice
	faults    bool
	addresses []uint64
}

func (d *registerBlockDevice) Read(ctx context.Context, addr uint64, data []byte) error {
	d.fail = d.faults && addr == 0x104
	d.addresses = append(d.addresses, addr)
	return d.registerBusDevice.Read(ctx, addr, data)
}
func (d *registerBlockDevice) Write(ctx context.Context, addr uint64, data []byte) error {
	d.fail = d.faults && addr == 0x104
	d.addresses = append(d.addresses, addr)
	return d.registerBusDevice.Write(ctx, addr, data)
}

func TestRegisterBlockFaultKeepsPrefixAndPeer(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) { exerciseRegisterBlockFault(t, write) })
	}
}

func exerciseRegisterBlockFault(t *testing.T, write bool) {
	t.Helper()
	model := newRegisterModel(t)
	sel, peerSel := dap.NewAPSel(1), dap.NewAPSel(2)
	device := &registerBlockDevice{faults: true}
	if err := model.target.MapMEMAPDevice(sel, 0x100, 12, device); err != nil {
		t.Fatal(err)
	}
	if err := model.target.AddMEMAP(peerSel, 0x24770011, map[uint32]uint32{0x100: 7}); err != nil {
		t.Fatal(err)
	}
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := dap.OpenMemAP(t.Context(), dp, peerSel)
	if err != nil {
		t.Fatal(err)
	}
	requireRegisterBlockFault(t, mem, device, write)
	device.faults = false
	if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatalf("selected client unusable: %v", err)
	}
	if value, err := peer.ReadWord(t.Context(), 0x100); err != nil || value != 7 {
		t.Fatalf("peer=%d,%v", value, err)
	}
	if err := peer.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func requireRegisterBlockFault(t *testing.T, mem *dap.MemAP, device *registerBlockDevice, write bool) {
	t.Helper()
	data := bytes.Repeat([]byte{0xcc}, 12)
	var n int
	var err error
	if write {
		n, err = mem.WriteBlock(t.Context(), 0x100, data)
	} else {
		n, err = mem.ReadBlock(t.Context(), 0x100, data)
	}
	if n != 4 || !errors.Is(err, dap.ErrFault) || errors.Is(err, dap.ErrIndeterminate) != write {
		t.Fatalf("block prefix=%d error=%v", n, err)
	}
	if len(device.addresses) != 2 || device.addresses[0] != 0x100 || device.addresses[1] != 0x104 {
		t.Fatalf("target access sequence=%#x", device.addresses)
	}
	if !write && (!bytes.Equal(data[:4], bytes.Repeat([]byte{42}, 4)) || !bytes.Equal(data[4:], bytes.Repeat([]byte{0xcc}, 8))) {
		t.Fatalf("read prefix/suffix=%x", data)
	}
}
