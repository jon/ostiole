package cmsisdap

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type registerCommandPeer struct {
	*swdCommandPeer
	target              *dapsim.Target
	wire                *swdsim.Wire
	ignoreOverrunWrites bool
	dataPhase           bool
	onRead              func(swdsim.Request, uint32)
}

func newRegisterCommandPeer(t *testing.T, overrun bool) *registerCommandPeer {
	t.Helper()
	target := dapsim.New(0x2ba01477)
	target.SetOverrunDetect(overrun)
	if err := target.AddMEMAP(dap.NewAPSel(1), 0x24770011, map[uint32]uint32{0x100: 42}); err != nil {
		t.Fatal(err)
	}
	peer := &registerCommandPeer{swdCommandPeer: newSWDCommandPeer(64), target: target, wire: swdsim.New(target)}
	peer.device.claim.handle = peer.handle
	return peer
}

func (p *registerCommandPeer) handle(command []byte) ([]byte, error) {
	switch command[0] {
	case 0x04:
		if len(command) != 6 || binary.LittleEndian.Uint16(command[2:4]) != 0 {
			return nil, errors.New("peer: firmware WAIT retries must be disabled")
		}
	case 0x13:
		if len(command) != 2 || command[1]&^byte(4) != 0 {
			return nil, errors.New("peer: invalid SWD configuration")
		}
		p.dataPhase = command[1]&4 != 0
	case 0x12:
		if len(command) < 3 {
			return nil, errors.New("peer: short SWJ sequence")
		}
		bits := int(command[1])
		if bits == 0 {
			bits = 256
		}
		direction := make([]byte, (bits+7)/8)
		for i := range direction {
			direction[i] = 0xff
		}
		if _, err := p.wire.SWDIO(context.Background(), direction, command[2:], bits); err != nil {
			return nil, err
		}
	case 5:
		return p.transfer(command)
	default:
		return p.swdCommandPeer.handle(command)
	}
	p.commands = append(p.commands, append([]byte(nil), command...))
	return []byte{command[0], 0}, nil
}

func (p *registerCommandPeer) transfer(command []byte) ([]byte, error) {
	p.commands = append(p.commands, append([]byte(nil), command...))
	if len(command) < 4 || command[1] != 0 || command[2] != 1 || command[3]&0xf0 != 0 {
		return nil, errors.New("peer: malformed single transfer")
	}
	req := swdsim.Request{AP: command[3]&1 != 0, Read: command[3]&2 != 0, Addr: command[3] & 12}
	if failure := p.acknowledge(req, 0); failure != nil {
		return failure, nil
	}
	if !req.Read {
		if len(command) != 8 {
			return nil, errors.New("peer: short register write")
		}
		previous := p.target.OverrunDetectEnabled()
		if err := p.target.Write(context.Background(), req, binary.LittleEndian.Uint32(command[4:])); err != nil {
			return nil, err
		}
		if p.ignoreOverrunWrites && !req.AP && req.Addr == 4 {
			p.target.SetOverrunDetect(previous)
		}
		if failure := p.acknowledge(swdsim.Request{Read: true, Addr: 12}, 1); failure != nil {
			return failure, nil
		}
		return []byte{5, 1, 1}, nil
	}
	value, err := p.target.Read(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if req.AP {
		if failure := p.acknowledge(swdsim.Request{Read: true, Addr: 12}, 1); failure != nil {
			return failure, nil
		}
		value, err = p.target.Read(context.Background(), swdsim.Request{Read: true, Addr: 12})
		if err != nil {
			return nil, err
		}
	}
	if p.onRead != nil {
		p.onRead(req, value)
	}
	return binary.LittleEndian.AppendUint32([]byte{5, 1, 1}, value), nil
}

func TestRegisterTransportKeepsProtocolAndHostCleanupSeparate(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		peer := newRegisterCommandPeer(t, inherited)
		session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		before := len(peer.commands)
		registers := NewRegisterSWD(session)
		if len(peer.commands) != before || registers.CleanupPending() {
			t.Fatal("constructor acquired protocol state")
		}
		dp := dap.NewDebugPort(dap.RegisterDP(registers))
		if _, err := dp.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !registers.CleanupPending() || peer.target.OverrunDetectEnabled() || peer.dataPhase {
			t.Fatal("register connection did not acquire simple-response ownership")
		}
		mem, err := dap.OpenMemAP(t.Context(), dp, dap.NewAPSel(1))
		if err != nil {
			t.Fatal(err)
		}
		if value, err := mem.ReadWord(t.Context(), 0x100); value != 42 || err != nil {
			t.Fatalf("memory = %d,%v", value, err)
		}
		if err := mem.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := dp.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if registers.CleanupPending() || !session.CleanupPending() || peer.target.OverrunDetectEnabled() != inherited || peer.dataPhase != inherited {
			t.Fatal("protocol release lost host ownership or original framing")
		}
	}
}

func TestRegisterEntryVerifiesDisabledOverrun(t *testing.T) {
	peer := newRegisterCommandPeer(t, true)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	registers := NewRegisterSWD(session)
	peer.ignoreOverrunWrites = true
	if _, err := registers.Connect(t.Context()); err == nil || !registers.CleanupPending() {
		t.Fatalf("unverified entry = %v, pending %t", err, registers.CleanupPending())
	}
	before := len(peer.commands)
	if _, status, err := registers.ReadAP(t.Context(), 12); err == nil || status != dap.RegisterUnsent || len(peer.commands) != before {
		t.Fatal("failed entry allowed ordinary register traffic")
	}
	peer.ignoreOverrunWrites = false
	if err := registers.Release(t.Context()); err != nil || registers.CleanupPending() || !peer.target.OverrunDetectEnabled() {
		t.Fatalf("entry cleanup = %v", err)
	}
}

func TestRegisterReleaseVerifiesRestoredOverrun(t *testing.T) {
	peer := newRegisterCommandPeer(t, true)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	registers := NewRegisterSWD(session)
	if _, err := registers.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	peer.ignoreOverrunWrites = true
	if err := registers.Release(t.Context()); err == nil || !registers.CleanupPending() {
		t.Fatalf("unverified restoration = %v, pending %t", err, registers.CleanupPending())
	}
	peer.ignoreOverrunWrites = false
	if err := registers.Release(t.Context()); err != nil || registers.CleanupPending() || !peer.target.OverrunDetectEnabled() {
		t.Fatalf("retried restoration = %v", err)
	}
	before := len(peer.commands)
	if err := registers.Release(t.Context()); err != nil || len(peer.commands) != before {
		t.Fatal("finished restoration repeated")
	}
}

func TestRegisterCommandsDoNotInferSequenceSupportFromVersion(t *testing.T) {
	for _, version := range []string{"0254", "0255", "2.1", "unrecognized"} {
		t.Run(version, func(t *testing.T) {
			peer := newRegisterCommandPeer(t, true)
			peer.device.claim.responses[infoProtocolVersion] = infoResponse([]byte(version + "\x00"))
			session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			registers := NewRegisterSWD(session)
			id, err := registers.Connect(t.Context())
			if err != nil || id != 0x2ba01477 {
				t.Fatalf("mandatory register entry = %#x,%v", id, err)
			}
			if err := registers.Release(t.Context()); err != nil || registers.CleanupPending() {
				t.Fatalf("release = %v", err)
			}
			if version != "2.1" {
				before := len(peer.commands)
				if _, err := session.SWDIO(t.Context(), []byte{0xff}, []byte{0xff}, 8); err == nil || len(peer.commands) != before {
					t.Fatal("version string was treated as proof of optional sequence support")
				}
			}
		})
	}
}

func TestRegisterReentryRetainsFirstRestorationSnapshot(t *testing.T) {
	peer := newRegisterCommandPeer(t, true)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	registers := NewRegisterSWD(session)
	for range 2 {
		if _, err := registers.Connect(t.Context()); err != nil || peer.target.OverrunDetectEnabled() {
			t.Fatalf("entry = %v", err)
		}
	}
	if err := registers.Release(t.Context()); err != nil || !peer.target.OverrunDetectEnabled() {
		t.Fatalf("release lost original setting: %v", err)
	}
}

func TestRegisterCompletedRestorationKeepsCancellationOnlyOnce(t *testing.T) {
	peer := newRegisterCommandPeer(t, true)
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	registers := NewRegisterSWD(session)
	if _, err := registers.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	peer.onRead = func(request swdsim.Request, value uint32) {
		if !request.AP && request.Addr == 4 && value&1 != 0 {
			cancel()
		}
	}
	if err := registers.Release(ctx); !errors.Is(err, context.Canceled) || registers.CleanupPending() || !peer.target.OverrunDetectEnabled() {
		t.Fatalf("completed release = %v, pending %t", err, registers.CleanupPending())
	}
	before := len(peer.commands)
	if err := registers.Release(t.Context()); err != nil || len(peer.commands) != before {
		t.Fatal("completed restoration or cancellation was replayed")
	}
}

func (p *registerCommandPeer) acknowledge(req swdsim.Request, count byte) []byte {
	err := p.target.Acknowledge(context.Background(), req)
	if err == nil {
		return nil
	}
	var response byte = 8
	if p.dataPhase == p.target.OverrunDetectEnabled() {
		if errors.Is(err, swd.ErrFault) {
			response = 4
		} else if errors.Is(err, swd.ErrWait) {
			response = 2
		}
	}
	return []byte{5, count, response}
}

type registerFaultDevice struct {
	fail     bool
	accesses int
}

func (d *registerFaultDevice) Read(_ context.Context, _ uint64, data []byte) error {
	d.accesses++
	if d.fail {
		return dapsim.ErrBusFault
	}
	for i := range data {
		data[i] = 42
	}
	return nil
}

func (d *registerFaultDevice) Write(_ context.Context, _ uint64, _ []byte) error {
	d.accesses++
	if d.fail {
		return dapsim.ErrBusFault
	}
	return nil
}

func TestRegisterCompletedReadFaultKeepsManagedClients(t *testing.T) {
	exerciseRegisterManagedFault(t, false)
}

func TestRegisterAcceptedWriteFaultKeepsManagedClients(t *testing.T) {
	exerciseRegisterManagedFault(t, true)
}

func exerciseRegisterManagedFault(t *testing.T, write bool) {
	t.Helper()
	peer := newRegisterCommandPeer(t, true)
	device := &registerFaultDevice{}
	sel := dap.NewAPSel(1)
	other := dap.NewAPSel(2)
	if err := peer.target.MapMEMAPDevice(sel, 0x100, 4, device); err != nil {
		t.Fatal(err)
	}
	if err := peer.target.AddMEMAP(other, 0x24770011, map[uint32]uint32{0x100: 7}); err != nil {
		t.Fatal(err)
	}
	session, err := openSession(t.Context(), peer.device, WithSWD(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	registers := NewRegisterSWD(session)
	dp := dap.NewDebugPort(dap.RegisterDP(registers))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	mem, err := dap.OpenMemAP(t.Context(), dp, sel)
	if err != nil {
		t.Fatal(err)
	}
	otherMem, err := dap.OpenMemAP(t.Context(), dp, other)
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
		t.Fatalf("completed read fault = %v, accesses %d", err, device.accesses)
	}
	device.fail = false
	if _, err := mem.ReadWord(t.Context(), 0x100); err != nil {
		t.Fatal(err)
	}
	if value, err := otherMem.ReadWord(t.Context(), 0x100); value != 7 || err != nil {
		t.Fatalf("peer = %d,%v", value, err)
	}
	if err := errors.Join(mem.Release(t.Context()), otherMem.Release(t.Context()), dp.Release(t.Context())); err != nil {
		t.Fatal(err)
	}
}
