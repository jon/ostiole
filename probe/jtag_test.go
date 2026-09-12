package probe

import (
	"context"
	"errors"
	"testing"
)

func TestJTAGBorrowAndProtocolExclusion(t *testing.T) {
	for _, swdFirst := range []bool{false, true} {
		b := &jtagBackend{}
		p := New(Info{}, b)
		if swdFirst {
			if _, err := p.SWD(t.Context(), SWDConfig{MaxClockHz: 1000}); err != nil {
				t.Fatal(err)
			}
			if _, err := p.JTAG(t.Context(), JTAGConfig{MaxClockHz: 1000}); err == nil || b.jtagOpens != 0 {
				t.Fatal("JTAG activated over SWD")
			}
			continue
		}
		w, err := p.JTAG(t.Context(), JTAGConfig{MaxClockHz: 1000})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.JTAGIO(t.Context(), nil, nil, 0); err != nil || b.calls != 1 || w.MaxTransferBits() != 64 {
			t.Fatal("JTAG did not delegate")
		}
		if _, err := p.SWD(t.Context(), SWDConfig{MaxClockHz: 1000}); err == nil || b.opens != 0 {
			t.Fatal("SWD activated over JTAG")
		}
		b.closeErr = errors.New("cleanup")
		if !errors.Is(p.Close(), b.closeErr) {
			t.Fatal("lost cleanup failure")
		}
		if _, err := w.JTAGIO(t.Context(), nil, nil, 0); err == nil || w.MaxTransferBits() != 0 {
			t.Fatal("borrow survived close")
		}
		b.closeErr = nil
		if err := p.Close(); err != nil || b.closes != 2 {
			t.Fatal("cleanup did not retry")
		}
	}
}

func TestJTAGActivationFailureAndPreflight(t *testing.T) {
	b := &jtagBackend{}
	p := New(Info{}, b)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx context.Context
		hz  uint32
	}{{nil, 1000}, {ctx, 1000}, {t.Context(), 999}} {
		if _, err := p.JTAG(tc.ctx, JTAGConfig{MaxClockHz: tc.hz}); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if b.jtagOpens != 0 || b.closes != 0 {
		t.Fatal("preflight touched backend")
	}
	unsupported := New(Info{}, &backend{})
	if _, err := unsupported.JTAG(t.Context(), JTAGConfig{MaxClockHz: 1000}); !errors.Is(err, ErrUnsupportedJTAG) {
		t.Fatalf("unsupported: %v", err)
	}
	b.openErr, b.closeErr = errors.New("activate"), errors.New("cleanup")
	_, err := p.JTAG(t.Context(), JTAGConfig{MaxClockHz: 1000})
	if !errors.Is(err, b.openErr) || !errors.Is(err, b.closeErr) || b.closes != 1 {
		t.Fatalf("lost activation cleanup: %v", err)
	}
	if _, err := p.JTAG(t.Context(), JTAGConfig{MaxClockHz: 1000}); err == nil || b.jtagOpens != 1 {
		t.Fatal("retried activation")
	}
}

type jtagBackend struct {
	backend
	jtagOpens int
}

func (b *jtagBackend) JTAG(context.Context, JTAGConfig) (JTAGWire, error) {
	b.jtagOpens++
	return b, b.openErr
}

func (b *jtagBackend) JTAGIO(context.Context, []byte, []byte, int) ([]byte, error) {
	b.calls++
	return nil, nil
}
