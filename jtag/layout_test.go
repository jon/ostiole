package jtag

import (
	"context"
	"testing"
)

func benchChain(t *testing.T) (*chainWire, *Conn, *Chain) {
	t.Helper()
	w := &chainWire{state: Reset, limit: 7, taps: []modelTAP{{ir: 4, id: 0x5ba00477}, {ir: 12, id: 0x14730093}}}
	a, err := IDCODE(4, 0x5ba00477)
	if err != nil {
		t.Fatal(err)
	}
	b, err := IDCODE(12, 0x14730093)
	if err != nil {
		t.Fatal(err)
	}
	c := New(w)
	chain, err := NewChain(c, Layout{a, b})
	if err != nil {
		t.Fatal(err)
	}
	return w, c, chain
}

func TestLayoutValidation(t *testing.T) {
	ctx := context.Background()
	w, _, chain := benchChain(t)
	w.taps[0].id = 0
	if err := chain.Connect(ctx); err == nil {
		t.Fatal("dummy matched real DAP")
	}
	w.taps[0].id = 0x5ba00477
	w.taps[1].badCapture = true
	if err := chain.Connect(ctx); err == nil {
		t.Fatal("invalid IR capture accepted")
	}
	if _, err := NewChain(New(w), Layout{{}}); err == nil {
		t.Fatal("zero spec accepted")
	}
	if _, err := IDCODE(1, 3); err == nil {
		t.Fatal("short IR accepted")
	}
	if _, err := IDCODE(4, 0); err == nil {
		t.Fatal("zero ID accepted")
	}
	if _, err := IDCODE(4, ^uint32(0)); err == nil {
		t.Fatal("terminator ID accepted")
	}
}

func TestLayoutPreflight(t *testing.T) {
	good, _ := IDCODE(4, 0x5ba00477)
	for _, layout := range []Layout{nil, {}, {{}}, make(Layout, 1025)} {
		if err := layout.Validate(); err == nil {
			t.Fatal("invalid layout passed preflight")
		}
	}
	if err := (Layout{good}).Validate(); err != nil {
		t.Fatal(err)
	}
	w, _, chain := benchChain(t)
	if err := chain.Layout().Validate(); err != nil || w.calls != 0 {
		t.Fatalf("layout preflight: %v, calls=%d", err, w.calls)
	}
}

func TestIRLengthMustMatchExactly(t *testing.T) {
	w, conn, _ := benchChain(t)
	a, _ := IDCODE(4, 0x5ba00477)
	b, _ := IDCODE(16, 0x14730093)
	chain, err := NewChain(conn, Layout{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.Connect(context.Background()); err == nil {
		t.Fatal("long final IR was accepted because the capture suffix was ones")
	}
	if w.taps[1].instruction != 0xfff {
		t.Fatal("failed measurement did not leave bypass")
	}
}

func TestShortLayoutDoesNotUpdateCapturedInstructions(t *testing.T) {
	w := &chainWire{state: Idle, limit: 31, taps: []modelTAP{{ir: 64, id: 3}}}
	spec, _ := IDCODE(2, 3)
	chain, err := NewChain(New(w), Layout{spec})
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.Connect(context.Background()); err == nil {
		t.Fatal("short layout accepted")
	}
	if w.taps[0].instruction != ^uint64(0) {
		t.Fatalf("unsafe IR update: %x", w.taps[0].instruction)
	}
}
