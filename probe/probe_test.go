package probe

import (
	"context"
	"errors"
	"testing"
)

func TestBorrowedWireFollowsOwner(t *testing.T) {
	b := &backend{}
	p := New(Info{Serial: "one"}, b)
	if b.opens != 0 || b.closes != 0 {
		t.Fatal("construction performed I/O")
	}
	w, err := p.SWD(t.Context(), SWDConfig{MaxClockHz: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.SWDIO(t.Context(), nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if w.MaxTransferBits() != 64 || b.calls != 1 {
		t.Fatal("wire did not delegate")
	}
	closeErr := errors.New("close")
	b.closeErr = closeErr
	if !errors.Is(p.Close(), closeErr) {
		t.Fatal("lost close failure")
	}
	if _, err := w.SWDIO(t.Context(), nil, nil, 0); err == nil {
		t.Fatal("closed wire usable")
	}
	if w.MaxTransferBits() != 0 {
		t.Fatal("closed wire has limits")
	}
	b.closeErr = nil
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil || b.closes != 2 {
		t.Fatal("close not idempotent")
	}
	if p.Info().Serial != "one" {
		t.Fatal("lost metadata")
	}
}

func TestActivationFailureRetainsCleanup(t *testing.T) {
	primary, cleanup := errors.New("activate"), errors.New("cleanup")
	b := &backend{openErr: primary, closeErr: cleanup}
	p := New(Info{}, b)
	_, err := p.SWD(t.Context(), SWDConfig{MaxClockHz: 1000})
	if !errors.Is(err, primary) || !errors.Is(err, cleanup) || b.closes != 1 {
		t.Fatalf("failure: %v", err)
	}
	if _, err := p.SWD(t.Context(), SWDConfig{MaxClockHz: 1000}); err == nil || b.opens != 1 {
		t.Fatal("retried activation")
	}
	b.closeErr = nil
	if err := p.Close(); err != nil || b.closes != 2 {
		t.Fatal("cleanup not retained")
	}
}

func TestSWDPreflightAndUnsupported(t *testing.T) {
	b := &backend{}
	p := New(Info{}, b)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx context.Context
		hz  uint32
	}{{nil, 1000}, {ctx, 1000}, {t.Context(), 999}} {
		if _, err := p.SWD(tc.ctx, SWDConfig{MaxClockHz: tc.hz}); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if b.opens != 0 || b.closes != 0 {
		t.Fatal("preflight touched backend")
	}
	unsupported := New(Info{}, &closeOnly{})
	if _, err := unsupported.SWD(t.Context(), SWDConfig{MaxClockHz: 1000}); !errors.Is(err, ErrUnsupportedSWD) {
		t.Fatalf("unsupported: %v", err)
	}
	var zero Probe
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := zero.SWD(t.Context(), SWDConfig{MaxClockHz: 1000}); err == nil {
		t.Fatal("zero owner activated")
	}
}

type closeOnly struct{}

func (*closeOnly) Close() error { return nil }

type backend struct {
	opens, closes, calls int
	openErr, closeErr    error
}

func (b *backend) Close() error                                 { b.closes++; return b.closeErr }
func (b *backend) SWD(context.Context, SWDConfig) (Wire, error) { b.opens++; return b, b.openErr }
func (b *backend) SWDIO(context.Context, []byte, []byte, int) ([]byte, error) {
	b.calls++
	return nil, nil
}
func (*backend) MaxTransferBits() int { return 64 }
