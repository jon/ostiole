package probe

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
)

type registerBackend struct {
	backend
	registers   dap.RegisterTransport
	activations int
	activateErr error
}

func (b *registerBackend) RegisterSWD(context.Context, SWDConfig) (dap.RegisterTransport, error) {
	b.activations++
	return b.registers, b.activateErr
}

type registerCalls struct {
	calls   []string
	addr    uint8
	data    uint32
	pending bool
}

func (r *registerCalls) Connect(context.Context) (uint32, error) {
	r.calls = append(r.calls, "connect")
	r.pending = true
	return 0x2ba01477, nil
}
func (r *registerCalls) Release(context.Context) error {
	r.calls = append(r.calls, "release")
	r.pending = false
	return nil
}
func (r *registerCalls) CleanupPending() bool { return r.pending }
func (r *registerCalls) ReadDP(_ context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	r.calls = append(r.calls, "readDP")
	r.addr = addr
	return 11, dap.RegisterCompleted, nil
}
func (r *registerCalls) ReadAP(_ context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	r.calls = append(r.calls, "readAP")
	r.addr = addr
	return 22, dap.RegisterCompleted, nil
}
func (r *registerCalls) WriteDP(_ context.Context, addr uint8, data uint32) (dap.RegisterStatus, error) {
	r.calls = append(r.calls, "writeDP")
	r.addr, r.data = addr, data
	return dap.RegisterCompleted, nil
}
func (r *registerCalls) WriteAP(_ context.Context, addr uint8, data uint32) (dap.RegisterStatus, error) {
	r.calls = append(r.calls, "writeAP")
	r.addr, r.data = addr, data
	return dap.RegisterRejected, dap.ErrWait
}

func TestBorrowedRegisterSWDFollowsOwner(t *testing.T) {
	calls := &registerCalls{}
	backend := &registerBackend{registers: calls}
	p := New(Info{}, backend)
	if !p.SupportsRegisterSWD() || backend.activations != 0 || backend.closes != 0 || len(calls.calls) != 0 {
		t.Fatal("capability preflight performed I/O")
	}
	surface, err := p.RegisterSWD(t.Context(), SWDConfig{MaxClockHz: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls.calls) != 0 || backend.activations != 1 {
		t.Fatal("activation entered the protocol")
	}
	if value, err := surface.Connect(t.Context()); err != nil || value != 0x2ba01477 || !surface.CleanupPending() {
		t.Fatalf("entry=%#x,%v", value, err)
	}
	checkRegisterDelegation(t, surface, calls)
	if _, err := p.SWD(t.Context(), SWDConfig{MaxClockHz: 1000}); err == nil || backend.opens != 0 {
		t.Fatal("activated wire alongside registers")
	}
	if err := surface.Release(t.Context()); err != nil || surface.CleanupPending() {
		t.Fatalf("release=%v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	requireRegisterBorrowClosed(t, p, surface, calls)
}

func requireRegisterBorrowClosed(t *testing.T, p *Probe, surface RegisterSWD, calls *registerCalls) {
	t.Helper()
	before := len(calls.calls)
	if p.SupportsRegisterSWD() {
		t.Fatal("released owner advertises capability")
	}
	if _, status, err := surface.ReadAP(t.Context(), 12); status != dap.RegisterUnsent || err == nil {
		t.Fatalf("closed read=%v,%v", status, err)
	}
	if status, err := surface.WriteDP(t.Context(), 8, 1); status != dap.RegisterUnsent || err == nil {
		t.Fatalf("closed write=%v,%v", status, err)
	}
	if _, status, err := surface.ReadDP(t.Context(), 4); status != dap.RegisterUnsent || err == nil {
		t.Fatalf("closed DP read=%v,%v", status, err)
	}
	if status, err := surface.WriteAP(t.Context(), 12, 1); status != dap.RegisterUnsent || err == nil {
		t.Fatalf("closed AP write=%v,%v", status, err)
	}
	if _, err := surface.Connect(t.Context()); err == nil {
		t.Fatal("closed entry succeeded")
	}
	if err := surface.Release(t.Context()); err == nil {
		t.Fatal("closed release succeeded")
	}
	if len(calls.calls) != before || surface.CleanupPending() {
		t.Fatal("closed borrow reached backend")
	}
}

func checkRegisterDelegation(t *testing.T, surface RegisterSWD, calls *registerCalls) {
	t.Helper()
	if value, status, err := surface.ReadDP(t.Context(), 4); value != 11 || status != dap.RegisterCompleted || err != nil || calls.addr != 4 {
		t.Fatalf("DP read=%d,%v,%v", value, status, err)
	}
	if value, status, err := surface.ReadAP(t.Context(), 12); value != 22 || status != dap.RegisterCompleted || err != nil || calls.addr != 12 {
		t.Fatalf("AP read=%d,%v,%v", value, status, err)
	}
	if status, err := surface.WriteDP(t.Context(), 8, 33); status != dap.RegisterCompleted || err != nil || calls.data != 33 || calls.addr != 8 {
		t.Fatalf("DP write=%v,%v", status, err)
	}
	if status, err := surface.WriteAP(t.Context(), 0, 44); status != dap.RegisterRejected || !errors.Is(err, dap.ErrWait) || calls.data != 44 || calls.addr != 0 {
		t.Fatalf("AP write=%v,%v", status, err)
	}
}

func TestRegisterSWDActivationPreflightAndCleanup(t *testing.T) {
	b := &registerBackend{registers: &registerCalls{}}
	p := New(Info{}, b)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx context.Context
		hz  uint32
	}{{nil, 1000}, {ctx, 1000}, {t.Context(), 999}} {
		if _, err := p.RegisterSWD(tc.ctx, SWDConfig{MaxClockHz: tc.hz}); err == nil {
			t.Fatal("invalid activation succeeded")
		}
	}
	if b.activations != 0 || b.closes != 0 {
		t.Fatal("invalid activation touched backend")
	}
	unsupported := New(Info{}, &closeOnly{})
	if unsupported.SupportsRegisterSWD() {
		t.Fatal("wire-only owner claims register support")
	}
	if _, err := unsupported.RegisterSWD(t.Context(), SWDConfig{MaxClockHz: 1000}); !errors.Is(err, ErrUnsupportedRegisterSWD) {
		t.Fatalf("unsupported=%v", err)
	}
	b.registers = nil
	b.closeErr = errors.New("close pending")
	if _, err := p.RegisterSWD(t.Context(), SWDConfig{MaxClockHz: 1000}); err == nil || !errors.Is(err, b.closeErr) || b.closes != 1 {
		t.Fatalf("nil surface=%v", err)
	}
	if p.SupportsRegisterSWD() {
		t.Fatal("closing owner claims availability")
	}
	b.closeErr = nil
	if err := p.Close(); err != nil || b.closes != 2 {
		t.Fatalf("cleanup=%v", err)
	}
}

func TestRegisterSWDRejectsTypedNilAndAcceptsValue(t *testing.T) {
	var transport *registerCalls
	b := &registerBackend{registers: transport}
	b.closeErr = errors.New("pending host cleanup")
	p := New(Info{}, b)
	if _, err := p.RegisterSWD(t.Context(), SWDConfig{MaxClockHz: 1000}); err == nil || !errors.Is(err, b.closeErr) || b.closes != 1 {
		t.Fatalf("typed nil surface=%v closes=%d", err, b.closes)
	}
	if p.SupportsRegisterSWD() {
		t.Fatal("failed activation still available")
	}
	b.closeErr = nil
	if err := p.Close(); err != nil || b.closes != 2 {
		t.Fatalf("close retry=%v calls=%d", err, b.closes)
	}
	calls := &registerCalls{}
	p = New(Info{}, &registerBackend{registers: struct{ *registerCalls }{calls}})
	surface, err := p.RegisterSWD(t.Context(), SWDConfig{MaxClockHz: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := surface.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := surface.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterBorrowInvalidatesAfterFailedHostClose(t *testing.T) {
	calls := &registerCalls{}
	b := &registerBackend{registers: calls}
	p := New(Info{}, b)
	surface, err := p.RegisterSWD(t.Context(), SWDConfig{MaxClockHz: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := surface.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := surface.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.closeErr = errors.New("host close pending")
	if err := p.Close(); !errors.Is(err, b.closeErr) || !p.CleanupPending() {
		t.Fatalf("host cleanup=%v", err)
	}
	requireRegisterBorrowClosed(t, p, surface, calls)
	b.closeErr = nil
	if err := p.Close(); err != nil || b.closes != 2 {
		t.Fatalf("host retry=%v calls=%d", err, b.closes)
	}
}

func TestZeroRegisterSWDSurfaceIsInactive(t *testing.T) {
	requireRegisterBorrowClosed(t, nil, RegisterSWD{}, &registerCalls{})
}
