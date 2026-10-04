package sim_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type deviceAccess struct {
	addr  uint64
	data  []byte
	write bool
}

type memoryDevice struct {
	data       map[uint64]byte
	accesses   []deviceAccess
	err        error
	errAt      uint64
	afterWrite bool
	ctx        context.Context
}

func (d *memoryDevice) Read(ctx context.Context, addr uint64, data []byte) error {
	d.ctx = ctx
	d.accesses = append(d.accesses, deviceAccess{addr: addr, data: make([]byte, len(data))})
	if d.err != nil && (d.errAt == 0 || d.errAt == addr) {
		return d.err
	}
	for i := range data {
		data[i] = d.data[addr+uint64(i)]
	}
	return nil
}

func (d *memoryDevice) Write(ctx context.Context, addr uint64, data []byte) error {
	d.ctx = ctx
	d.accesses = append(d.accesses, deviceAccess{addr: addr, data: slices.Clone(data), write: true})
	if !d.afterWrite && d.err != nil && (d.errAt == 0 || d.errAt == addr) {
		return d.err
	}
	if d.data == nil {
		d.data = make(map[uint64]byte)
	}
	for i, b := range data {
		d.data[addr+uint64(i)] = b
	}
	if d.afterWrite && (d.errAt == 0 || d.errAt == addr) {
		return d.err
	}
	return nil
}

func mappedTarget(t *testing.T, addr, size uint64, device dapsim.MemoryDevice) *dapsim.Target {
	t.Helper()
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, nil)
	if err := target.MapMEMAPDevice(apSel(0), addr, size, device); err != nil {
		t.Fatal(err)
	}
	return target
}

func openedMemory(t *testing.T, target *dapsim.Target, sel dap.APSel) *dap.MemAP {
	t.Helper()
	dp := enteredDAP(t, target)
	memory, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := memory.Release(context.Background()); err != nil {
			t.Errorf("release MEM-AP: %v", err)
		}
	})
	return memory
}

func TestMappedDevicePreservesScalarWidthAndByteOrder(t *testing.T) {
	for _, bigEndian := range []bool{false, true} {
		for _, size := range []dap.TransferSize{dap.Size8, dap.Size16, dap.Size32, dap.Size64} {
			t.Run(sizeName(size, bigEndian), func(t *testing.T) {
				testMappedScalar(t, size, bigEndian)
			})
		}
	}
}

func testMappedScalar(t *testing.T, size dap.TransferSize, bigEndian bool) {
	t.Helper()
	device := new(memoryDevice)
	target := mappedTarget(t, 0x100, 16, device)
	cfg := uint32(4)
	if bigEndian {
		cfg |= 1
	}
	if err := target.SetMEMAPCFG(apSel(0), cfg); err != nil {
		t.Fatal(err)
	}
	if err := target.SetMEMAPSizes(apSel(0), dap.Size8, dap.Size16, dap.Size32, dap.Size64); err != nil {
		t.Fatal(err)
	}
	memory := openedMemory(t, target, apSel(0))
	addr := uint64(0x108)
	if size == dap.Size8 {
		addr++
	}
	if size == dap.Size16 {
		addr += 2
	}
	value := uint64(0x8877665544332211)
	if size != dap.Size64 {
		value &= uint64(1)<<(uint(size)*8) - 1
	}
	if err := memory.WriteScalar(t.Context(), addr, size, value); err != nil {
		t.Fatal(err)
	}
	got, err := memory.ReadScalar(t.Context(), addr, size)
	if err != nil || got != value {
		t.Fatalf("ReadScalar = %#x, %v; want %#x", got, err, value)
	}
	checkScalarAccesses(t, device.accesses, addr, size, value, bigEndian)
}

func checkScalarAccesses(t *testing.T, accesses []deviceAccess, addr uint64, size dap.TransferSize, value uint64, bigEndian bool) {
	t.Helper()
	if len(accesses) != 2 {
		t.Fatalf("device accesses = %v, want one write and one read", accesses)
	}
	write, read := accesses[0], accesses[1]
	if !write.write || read.write || write.addr != addr || read.addr != addr || len(write.data) != int(size) || len(read.data) != int(size) {
		t.Fatalf("device accesses lost direction, address or width: %v", accesses)
	}
	for i, b := range write.data {
		shift := i * 8
		if bigEndian {
			shift = (int(size) - 1 - i) * 8
		}
		if want := byte(value >> uint(shift)); b != want {
			t.Fatalf("byte %d = %#x, want %#x", i, b, want)
		}
	}
}

func sizeName(size dap.TransferSize, bigEndian bool) string {
	name := map[dap.TransferSize]string{dap.Size8: "byte", dap.Size16: "halfword", dap.Size32: "word", dap.Size64: "doubleword"}[size]
	if bigEndian {
		return name + "/big"
	}
	return name + "/little"
}

func TestMappedDeviceRejectsInvalidMappings(t *testing.T) {
	device := new(memoryDevice)
	target := mappedTarget(t, 0x100, 4, device)
	var nilDevice *memoryDevice
	for _, test := range []struct {
		name       string
		sel        dap.APSel
		addr, size uint64
		device     dapsim.MemoryDevice
	}{
		{"zero selector", dap.APSel{}, 0, 4, device},
		{"absent AP", apSel(1), 0, 4, device},
		{"empty", apSel(0), 0, 0, device},
		{"overflow", apSel(0), math.MaxUint64, 2, device},
		{"nil", apSel(0), 0, 4, nil},
		{"typed nil", apSel(0), 0, 4, nilDevice},
		{"overlap", apSel(0), 0xff, 2, device},
		{"contained", apSel(0), 0x101, 1, device},
		{"containing", apSel(0), 0, 0x200, device},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := target.MapMEMAPDevice(test.sel, test.addr, test.size, test.device); err == nil {
				t.Fatal("accepted invalid mapping")
			}
		})
	}
	if err := (*dapsim.Target)(nil).MapMEMAPDevice(apSel(0), 0, 4, device); err == nil {
		t.Fatal("nil target accepted mapping")
	}
	if err := target.MapMEMAPDevice(apSel(0), 0x104, 4, device); err != nil {
		t.Fatalf("adjacent mapping: %v", err)
	}
	if err := target.MapMEMAPDevice(apSel(0), math.MaxUint64, 1, device); err != nil {
		t.Fatalf("last address: %v", err)
	}
	_ = enteredDAP(t, target)
	if err := target.MapMEMAPDevice(apSel(0), 0x200, 4, device); err == nil {
		t.Fatal("mapping changed after traffic")
	}
}

func TestMappedDeviceFixtureAccessCannotBypassDevice(t *testing.T) {
	device := new(memoryDevice)
	target := mappedTarget(t, 0x102, 2, device)
	for _, addr := range []uint64{0x100, 0x102, 0x103} {
		if err := target.SetMEMAPBytes(apSel(0), addr, []byte{1, 2, 3, 4}); err == nil {
			t.Fatalf("fixture write at %#x bypassed device", addr)
		}
		if _, err := target.MEMAPBytes(apSel(0), addr, 4); err == nil {
			t.Fatalf("fixture read at %#x bypassed device", addr)
		}
	}
	if err := target.SetMEMAPBytes(apSel(0), 0x104, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if len(device.accesses) != 0 {
		t.Fatal("fixture access invoked device")
	}
}

func TestMappedDeviceCrossingAccessFaultsBeforeEffects(t *testing.T) {
	for _, read := range []bool{true, false} {
		device := new(memoryDevice)
		target := mappedTarget(t, 0x102, 2, device)
		memory := openedMemory(t, target, apSel(0))
		var err error
		if read {
			_, err = memory.ReadWord(t.Context(), 0x100)
		} else {
			err = memory.WriteWord(t.Context(), 0x100, 7)
		}
		if !errors.Is(err, dap.ErrFault) {
			t.Fatalf("crossing access error = %v, want DAP fault", err)
		}
		if len(device.accesses) != 0 {
			t.Fatal("crossing access reached device")
		}
		data, err := target.MEMAPBytes(apSel(0), 0x100, 2)
		if err != nil || !slices.Equal(data, []byte{0, 0}) {
			t.Fatalf("ordinary prefix changed: %v, %v", data, err)
		}
	}
}

func TestMappedDeviceFaultUsesDAPRecovery(t *testing.T) {
	device := &memoryDevice{err: dapsim.ErrBusFault}
	target := mappedTarget(t, 0x100, 4, device)
	dp := enteredDAP(t, target)
	memory, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = memory.ReadWord(t.Context(), 0x100)
	var fault *dap.FaultError
	if !errors.As(err, &fault) || !fault.StateValid || fault.CTRLSTAT&(1<<5) == 0 {
		t.Fatalf("read fault = %v, want captured STICKYERR", err)
	}
	if err := memory.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	memory, err = dap.OpenMemAP(t.Context(), dp, apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := memory.Release(context.Background()); err != nil {
			t.Error(err)
		}
	})
	device.err = nil
	if err := memory.WriteWord(t.Context(), 0x100, 42); err != nil {
		t.Fatalf("write after fault recovery: %v", err)
	}
	if got, err := memory.ReadWord(t.Context(), 0x100); err != nil || got != 42 {
		t.Fatalf("read after recovery = %d, %v", got, err)
	}
}

func TestMappedDeviceReceivesContextAndCancellation(t *testing.T) {
	device := new(memoryDevice)
	target := mappedTarget(t, 0x100, 4, device)
	memory := openedMemory(t, target, apSel(0))
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := memory.ReadWord(ctx, 0x100); err != nil {
		t.Fatal(err)
	}
	if device.ctx != ctx {
		t.Fatal("device lost caller context")
	}
	cancel()
	before := len(device.accesses)
	if err := memory.WriteWord(ctx, 0x100, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write = %v", err)
	}
	if len(device.accesses) != before {
		t.Fatal("canceled write reached device")
	}
}

func TestMappedDeviceFaultFollowsAcceptedWrite(t *testing.T) {
	for _, after := range []bool{false, true} {
		device := &memoryDevice{err: dapsim.ErrBusFault, afterWrite: after}
		target := mappedTarget(t, 0x100, 4, device)
		conn := enteredConn(t, target)
		selectAP(t, conn, 0, 0)
		if err := conn.WriteAP(t.Context(), 0, 2|0x10); err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteAP(t.Context(), 4, 0x100); err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteAP(t.Context(), 0x0c, 42); err != nil {
			t.Fatalf("accepted DRW write returned host error: %v", err)
		}
		ctrl, err := conn.ReadDP(t.Context(), 4)
		if err != nil || ctrl&(1<<5) == 0 {
			t.Fatalf("CTRL/STAT = %#x, %v; want STICKYERR", ctrl, err)
		}
		if _, err := conn.ReadDP(t.Context(), 0x0c); !errors.Is(err, swd.ErrFault) {
			t.Fatalf("completion = %v, want SWD FAULT", err)
		}
		if len(device.accesses) != 1 {
			t.Fatalf("faulting write was replayed: %v", device.accesses)
		}
		want := byte(0)
		if after {
			want = 42
		}
		if device.data[0x100] != want {
			t.Fatalf("write effect = %d, want %d", device.data[0x100], want)
		}
		if err := conn.WriteDP(t.Context(), 0, 0x1e); err != nil {
			t.Fatal(err)
		}
		if tar := readPosted(t, conn, 0, 0, 4); tar != 0x100 {
			t.Fatalf("fault incremented TAR: %#x", tar)
		}
	}
}

func TestMappedDeviceModelErrorIsNotBusFault(t *testing.T) {
	for _, failure := range []error{context.Canceled, errors.New("device model failed")} {
		device := &memoryDevice{err: failure}
		target := mappedTarget(t, 0x100, 4, device)
		conn := enteredConn(t, target)
		selectAP(t, conn, 0, 0)
		if err := conn.WriteAP(t.Context(), 0, 2); err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteAP(t.Context(), 4, 0x100); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ReadAP(t.Context(), 0x0c); !errors.Is(err, dapsim.ErrDeviceFailure) || errors.Is(err, swd.ErrFault) {
			t.Fatalf("model failure = %v, want device failure", err)
		}
		ctrl, err := target.Read(t.Context(), swdsim.Request{Read: true, Addr: 4})
		if err != nil || ctrl&(1<<5) != 0 {
			t.Fatalf("model failure set STICKYERR: %#x, %v", ctrl, err)
		}
	}
}

func TestMappedDeviceBlockCrossesTARBoundary(t *testing.T) {
	device := new(memoryDevice)
	target := mappedTarget(t, 0x3fc, 12, device)
	memory := openedMemory(t, target, apSel(0))
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	if n, err := memory.WriteBlock(t.Context(), 0x3fc, data); err != nil || n != len(data) {
		t.Fatalf("WriteBlock = %d, %v", n, err)
	}
	got := make([]byte, len(data))
	if n, err := memory.ReadBlock(t.Context(), 0x3fc, got); err != nil || n != len(got) || !slices.Equal(got, data) {
		t.Fatalf("ReadBlock = %d, %v, %v", n, err, got)
	}
	for i, access := range device.accesses {
		if want := uint64(0x3fc + (i%3)*4); access.addr != want || len(access.data) != 4 {
			t.Fatalf("access %d = %v, want word at %#x", i, access, want)
		}
	}
}

func TestMappedDeviceBlockFaultRetainsConfirmedPrefix(t *testing.T) {
	device := &memoryDevice{data: map[uint64]byte{0x3fc: 1}, err: dapsim.ErrBusFault, errAt: 0x400}
	target := mappedTarget(t, 0x3fc, 12, device)
	memory := openedMemory(t, target, apSel(0))
	got := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	n, err := memory.ReadBlock(t.Context(), 0x3fc, got)
	if n != 4 || !errors.Is(err, dap.ErrFault) || !slices.Equal(got[:4], []byte{1, 0, 0, 0}) {
		t.Fatalf("ReadBlock = %d, %v, %v; want four confirmed bytes and FAULT", n, err, got)
	}
	for _, b := range got[4:] {
		if b != 0xff {
			t.Fatal("fault changed unconfirmed suffix")
		}
	}
	if len(device.accesses) != 2 {
		t.Fatalf("access after fault or replay: %v", device.accesses)
	}
}

func TestMappedDeviceReceivesLargeAddress(t *testing.T) {
	const addr = uint64(0x12300000100)
	device := new(memoryDevice)
	target := mappedTarget(t, addr, 4, device)
	if err := target.SetMEMAPCFG(apSel(0), 2); err != nil {
		t.Fatal(err)
	}
	memory := openedMemory(t, target, apSel(0))
	if err := memory.WriteScalar(t.Context(), addr, dap.Size32, 7); err != nil {
		t.Fatal(err)
	}
	if len(device.accesses) != 1 || device.accesses[0].addr != addr {
		t.Fatalf("large address truncated: %v", device.accesses)
	}
}

func TestMappedDeviceBlockWriteFaultKeepsEarlierEffects(t *testing.T) {
	device := &memoryDevice{err: dapsim.ErrBusFault, errAt: 0x400}
	target := mappedTarget(t, 0x3fc, 12, device)
	memory := openedMemory(t, target, apSel(0))
	n, err := memory.WriteBlock(t.Context(), 0x3fc, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	if n != 4 || !errors.Is(err, dap.ErrFault) {
		t.Fatalf("WriteBlock = %d, %v; want four confirmed bytes and FAULT", n, err)
	}
	if len(device.accesses) != 2 || device.data[0x3fc] != 1 || device.data[0x400] != 0 || device.data[0x404] != 0 {
		t.Fatalf("unexpected effects or replay: %v, %v", device.accesses, device.data)
	}
}

type wordDevice struct {
	memoryDevice
	rejected int
}

func (d *wordDevice) Write(ctx context.Context, addr uint64, data []byte) error {
	if len(data) != 4 {
		d.rejected++
		return dapsim.ErrBusFault
	}
	return d.memoryDevice.Write(ctx, addr, data)
}

func TestMappedDeviceRejectsNarrowWriteWithoutReadModifyWrite(t *testing.T) {
	device := new(wordDevice)
	target := mappedTarget(t, 0x100, 4, device)
	memory := openedMemory(t, target, apSel(0))
	if err := memory.WriteScalar(t.Context(), 0x101, dap.Size8, 7); !errors.Is(err, dap.ErrFault) {
		t.Fatalf("narrow write = %v, want FAULT", err)
	}
	if device.rejected != 1 || len(device.accesses) != 0 {
		t.Fatalf("narrow write caused hidden access: %d, %v", device.rejected, device.accesses)
	}
}

func TestLineResetFreezesDeviceMappings(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	addMEMAPFixture(t, target, 0, memAPIDR, nil)
	target.ObserveLineReset()
	if err := target.MapMEMAPDevice(apSel(0), 0x100, 4, new(memoryDevice)); err == nil {
		t.Fatal("mapping accepted after line reset traffic")
	}
}

func TestMappedDeviceBlockCancellationKeepsConfirmedPrefix(t *testing.T) {
	device := &memoryDevice{data: map[uint64]byte{0x3fc: 1}, err: context.Canceled, errAt: 0x400}
	target := mappedTarget(t, 0x3fc, 12, device)
	memory := openedMemory(t, target, apSel(0))
	got := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	n, err := memory.ReadBlock(t.Context(), 0x3fc, got)
	if n != 4 || !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrFault) {
		t.Fatalf("ReadBlock = %d, %v; want four confirmed bytes and cancellation", n, err)
	}
	if !slices.Equal(got, []byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}) || len(device.accesses) != 2 {
		t.Fatalf("unconfirmed effects or replay: %v, %v", got, device.accesses)
	}
}

type onceFailingDevice struct {
	memoryDevice
	failure error
}

func (d *onceFailingDevice) Write(ctx context.Context, addr uint64, data []byte) error {
	if err := d.memoryDevice.Write(ctx, addr, data); err != nil {
		return err
	}
	if len(d.accesses) == 1 {
		return d.failure
	}
	return nil
}

func TestDeviceCallbackFailureCannotRejectOrReplayAcceptedAPWrite(t *testing.T) {
	for _, failure := range []error{swd.ErrWait, swd.ErrFault, swd.ErrParity, swd.ErrProtocol, swd.ErrNotExecuted} {
		t.Run(failure.Error(), func(t *testing.T) {
			device := new(onceFailingDevice)
			target := mappedTarget(t, 0x100, 4, device)
			dp := enteredDAP(t, target)
			memory, err := dap.OpenMemAP(t.Context(), dp, apSel(0))
			if err != nil {
				t.Fatal(err)
			}
			if err := memory.WriteWord(t.Context(), 0x100, 0); err != nil {
				t.Fatal(err)
			}
			device.accesses = nil
			device.failure = failure
			err = dp.WriteRawAP(t.Context(), apSel(0).Address(0x0c), 7)
			if !errors.Is(err, dapsim.ErrDeviceFailure) || errors.Is(err, failure) {
				t.Fatalf("callback failure = %v, want host/model failure without protocol classification", err)
			}
			if len(device.accesses) != 1 || device.data[0x100] != 7 {
				t.Fatalf("accepted write replayed or lost: %v, %v", device.accesses, device.data)
			}
		})
	}
}

func TestDeviceCallbackFailureCannotBecomeReadAcknowledgement(t *testing.T) {
	for _, failure := range []error{swd.ErrWait, swd.ErrFault, swd.ErrParity, swd.ErrProtocol, swd.ErrNotExecuted} {
		device := &memoryDevice{err: failure}
		target := mappedTarget(t, 0x100, 4, device)
		dp := enteredDAP(t, target)
		if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 2); err != nil {
			t.Fatal(err)
		}
		if err := dp.WriteRawAP(t.Context(), apSel(0).Address(4), 0x100); err != nil {
			t.Fatal(err)
		}
		_, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x0c))
		if !errors.Is(err, dapsim.ErrDeviceFailure) || errors.Is(err, failure) || len(device.accesses) != 1 {
			t.Fatalf("callback read = %v, %v; want one host/model failure", err, device.accesses)
		}
	}
}
