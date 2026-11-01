package cmsisdap

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/internal/probeusb"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/target/cortexm"
	coresim "github.com/jon/ostiole/target/cortexm/sim"
	"github.com/jon/ostiole/usb"
)

// commandProbe supplies the behavioral USB attachment to the real CMSIS factory.
// Protocol requests still pass through Session, RegisterSWD, DAP and MEM-AP.
type commandProbe struct {
	peer                         *registerCommandPeer
	session                      probeusb.RegisterSession
	activations, wireActivations int
}

func (p *commandProbe) RegisterSWD(ctx context.Context, config probe.SWDConfig) (dap.RegisterTransport, error) {
	p.activations++
	session, err := openRegisterProbeSession(ctx, p.peer.device, config)
	if session == nil {
		return nil, err
	}
	p.session = session
	return session.Registers(), err
}
func (p *commandProbe) SWD(context.Context, probe.SWDConfig) (probe.Wire, error) {
	p.wireActivations++
	return nil, errors.New("unexpected wire fallback")
}
func (p *commandProbe) Close() error {
	if p.session != nil {
		return p.session.Close()
	}
	return p.peer.device.Close()
}
func (p *commandProbe) CleanupPending() bool {
	if p.session != nil {
		return p.session.(probe.CleanupState).CleanupPending()
	}
	return p.peer.device.CleanupPending()
}
func (p *commandProbe) RestorationPossible() bool {
	if p.session != nil {
		return p.session.(probe.RestorationState).RestorationPossible()
	}
	return true
}

func connectCommandProbe(t *testing.T, peer *registerCommandPeer) (*armdebug.Conn, *commandProbe) {
	t.Helper()
	backend := &commandProbe{peer: peer}
	owner, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, backend), armdebug.Config{Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 1_000_000})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if backend.activations != 1 || backend.wireActivations != 0 {
		t.Fatal("register activation fell back to wire")
	}
	return owner, backend
}

func TestCMSISRegisterMemoryCompositionBothByteOrders(t *testing.T) {
	for _, big := range []bool{false, true} {
		t.Run(map[bool]string{false: "LE", true: "BE"}[big], func(t *testing.T) {
			exerciseCMSISMemoryOrder(t, big)
		})
	}
}

func exerciseCMSISMemoryOrder(t *testing.T, big bool) {
	t.Helper()
	peer := newRegisterCommandPeer(t, true)
	sel := dap.NewAPSel(1)
	cfg := uint32(6)
	var order binary.ByteOrder = binary.LittleEndian
	if big {
		cfg |= 1
		order = binary.BigEndian
	}
	if err := peer.target.SetMEMAPCFG(sel, cfg); err != nil {
		t.Fatal(err)
	}
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	port := dap.NewDebugPort(dap.RegisterDP(NewRegisterSWD(session)))
	var mem *dap.MemAP
	t.Cleanup(func() { cleanupCMSISMemory(t, mem, port, session) })
	if _, err := port.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	for addr, value := range map[uint8]uint32{0: 0xa5000051, 4: 0x9988, 8: 1} {
		if err := port.WriteRawAP(t.Context(), sel.Address(uint16(addr)), value); err != nil {
			t.Fatal(err)
		}
	}
	mem, err = dap.OpenMemAP(t.Context(), port, sel)
	if err != nil {
		t.Fatal(err)
	}
	exerciseCMSISScalars(t, peer, mem, order)
	exerciseCMSISBlock(t, peer, mem)
	if err := mem.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[uint8]uint32{0: 0xa5000051, 4: 0x9988, 8: 1} {
		if value, err := port.ReadRawAP(t.Context(), sel.Address(uint16(addr))); value != want || err != nil {
			t.Fatalf("restored AP %#x=%#x,%v", addr, value, err)
		}
	}
	if err := port.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil || !peer.target.OverrunDetectEnabled() || peer.device.closes != 1 {
		t.Fatalf("owner cleanup=%v", err)
	}
}

func cleanupCMSISMemory(t *testing.T, mem *dap.MemAP, port *dap.DebugPort, session *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if mem != nil {
		if err := mem.Release(ctx); err != nil {
			t.Error(err)
			return
		}
	}
	if err := port.Release(ctx); err != nil {
		t.Error(err)
		return
	}
	if err := session.Close(); err != nil {
		t.Error(err)
	}
}

func exerciseCMSISScalars(t *testing.T, peer *registerCommandPeer, mem *dap.MemAP, order binary.ByteOrder) {
	t.Helper()
	value := uint64(0x8765432101234567)
	for _, size := range []dap.TransferSize{dap.Size8, dap.Size16, dap.Size32, dap.Size64} {
		width := int(size)
		want := value
		if width < 8 {
			want &= uint64(1)<<(width*8) - 1
		}
		address := uint64(0x1_00000200)
		if err := mem.WriteScalar(t.Context(), address, size, want); err != nil {
			t.Fatal(err)
		}
		encoded := make([]byte, 8)
		order.PutUint64(encoded, want)
		if order == binary.BigEndian {
			encoded = encoded[8-width:]
		} else {
			encoded = encoded[:width]
		}
		actual, err := peer.target.MEMAPBytes(dap.NewAPSel(1), address, width)
		if err != nil || !slices.Equal(actual, encoded) {
			t.Fatalf("scalar bytes=%x,%v; want %x", actual, err, encoded)
		}
		if got, err := mem.ReadScalar(t.Context(), address, size); got != want || err != nil {
			t.Fatalf("scalar=%#x,%v", got, err)
		}
	}
}

func exerciseCMSISBlock(t *testing.T, peer *registerCommandPeer, mem *dap.MemAP) {
	t.Helper()
	data := make([]byte, 1031)
	for i := range data {
		data[i] = byte(i * 17)
	}
	address := uint64(0x1_000003fd)
	if n, err := mem.WriteBlock(t.Context(), address, data); n != len(data) || err != nil {
		t.Fatalf("block write=%d,%v", n, err)
	}
	actual, err := peer.target.MEMAPBytes(dap.NewAPSel(1), address, len(data))
	if err != nil || !slices.Equal(actual, data) {
		t.Fatalf("backing block differs: %v", err)
	}
	got := make([]byte, len(data))
	if n, err := mem.ReadBlock(t.Context(), address, got); n != len(data) || err != nil || !slices.Equal(got, data) {
		t.Fatalf("block read=%d,%v", n, err)
	}
}

func mapCommandCores(t *testing.T, peer *registerCommandPeer) []*coresim.Core {
	t.Helper()
	cores := make([]*coresim.Core, 2)
	for i := range cores {
		sel := dap.NewAPSel(uint8(i + 1))
		if i != 0 {
			if err := peer.target.AddMEMAP(sel, 0x24770011, nil); err != nil {
				t.Fatal(err)
			}
		}
		core, err := coresim.New(coresim.Config{Profile: coresim.M0})
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range []uint64{0xe000ed00, 0xe000ed30, 0xe000edf0} {
			if err := peer.target.MapMEMAPDevice(sel, address, 4, core); err != nil {
				t.Fatal(err)
			}
		}
		cores[i] = core
	}
	return cores
}

func TestCMSISRegisterTargetAndGroupControl(t *testing.T) {
	peer := newRegisterCommandPeer(t, true)
	cores := mapCommandCores(t, peer)
	owner, _ := connectCommandProbe(t, peer)
	first, err := owner.OpenMemAP(t.Context(), dap.NewAPSel(1))
	if err != nil {
		t.Fatal(err)
	}
	target, err := cortexm.Acquire(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Halt(t.Context()); err != nil || cores[0].Snapshot().DHCSR&(1<<17) == 0 {
		t.Fatalf("halt=%v", err)
	}
	if halted, err := target.Halted(t.Context()); !halted || err != nil {
		t.Fatalf("halted=%t,%v", halted, err)
	}
	if err := target.Resume(t.Context()); err != nil || cores[0].Snapshot().DHCSR&(1<<17) != 0 {
		t.Fatalf("resume=%v", err)
	}
	if err := target.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	exerciseCMSISGroup(t, owner, cores, first)
	if err := owner.Close(); err != nil || !peer.target.OverrunDetectEnabled() || peer.device.closes != 1 {
		t.Fatalf("owner cleanup=%v", err)
	}
}

func exerciseCMSISGroup(t *testing.T, owner *armdebug.Conn, cores []*coresim.Core, first *dap.MemAP) {
	t.Helper()
	second, err := owner.OpenMemAP(t.Context(), dap.NewAPSel(2))
	if err != nil {
		t.Fatal(err)
	}
	group, err := cortexm.AcquireGroup(t.Context(), []cortexm.Member{{ID: 1, Memory: first}, {ID: 2, Memory: second}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := group.Halt(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	for _, core := range cores {
		if core.Snapshot().DHCSR&(1<<17) == 0 {
			t.Fatal("group failed to halt")
		}
	}
	statuses, err := group.Status(t.Context(), 1, 2)
	if err != nil || len(statuses) != 2 {
		t.Fatalf("status=%v,%v", statuses, err)
	}
	for _, status := range statuses {
		if status.State != cortexm.Halted || !status.HaltOwned {
			t.Fatalf("halt ownership=%+v", status)
		}
	}
	if _, err := group.Resume(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := group.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, core := range cores {
		if core.Snapshot().DHCSR&3 != 0 {
			t.Fatal("group did not restore debug state")
		}
	}
}

func TestCMSISRegisterTerminalLossAbandonsTargetAndClosesHost(t *testing.T) {
	peer := newRegisterCommandPeer(t, true)
	cores := mapCommandCores(t, peer)
	owner, backend := connectCommandProbe(t, peer)
	mem, err := owner.OpenMemAP(t.Context(), dap.NewAPSel(1))
	if err != nil {
		t.Fatal(err)
	}
	target, err := cortexm.Acquire(t.Context(), mem)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Halt(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot := cores[0].Snapshot()
	peer.device.claim.inputErr = usb.ErrDeviceGone
	releaseErr := target.Release(t.Context())
	if !errors.Is(releaseErr, usb.ErrDeviceGone) || backend.RestorationPossible() {
		t.Fatalf("terminal release=%v", releaseErr)
	}
	before := len(peer.commands)
	final := errors.New("final host cleanup error")
	peer.device.closeErrs = []error{final}
	err = errors.Join(releaseErr, owner.Close())
	if !errors.Is(err, usb.ErrDeviceGone) || !errors.Is(err, armdebug.ErrRestorationAbandoned) || !errors.Is(err, final) {
		t.Fatalf("abandoned cleanup=%v", err)
	}
	if len(peer.commands) != before || cores[0].Snapshot() != snapshot || peer.device.closes != 1 || peer.device.claim.closes != 1 {
		t.Fatal("terminal close changed target or lost host cleanup")
	}
	if err := owner.Close(); err != nil || peer.device.closes != 1 {
		t.Fatalf("replayed close=%v", err)
	}
}

func TestCMSISRegisterCapabilityFailureDoesNotFallBack(t *testing.T) {
	peer := newRegisterCommandPeer(t, false)
	peer.device.claim.responses[infoCapabilities] = infoResponse([]byte{0x10})
	backend := &commandProbe{peer: peer}
	opened := probe.New(probe.Info{}, backend)
	owner, err := armdebug.Connect(t.Context(), opened, armdebug.Config{Port: armdebug.SWDP(probe.SWDConfig{MaxClockHz: 1_000_000})})
	if err == nil || owner != nil || backend.activations != 1 || backend.wireActivations != 0 || len(peer.effectCommands()) != 0 {
		t.Fatalf("unsupported activation owner=%v, error=%v, registers=%d, wire=%d", owner, err, backend.activations, backend.wireActivations)
	}
	if peer.device.closes != 1 || peer.device.claim.closes != 1 || opened.CleanupPending() {
		t.Fatal("unsupported activation lost host cleanup")
	}
	if err := opened.Close(); err != nil || peer.device.closes != 1 {
		t.Fatalf("unsupported close repeated=%v", err)
	}
}
