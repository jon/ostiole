package jtag

import (
	"context"
	"errors"
	"testing"
)

func TestSelectedTAPAndInvalidation(t *testing.T) {
	w, conn, chain := benchChain(t)
	ctx := context.Background()
	if w.calls != 0 {
		t.Fatal("constructor clocked")
	}
	if _, err := chain.TAP(0); err == nil {
		t.Fatal("lent unvalidated TAP")
	}
	if err := chain.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tap, err := chain.TAP(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tap.ScanIR(ctx, []byte{0xe}); err != nil {
		t.Fatal(err)
	}
	got, err := tap.ScanDR(ctx, make([]byte, 4), 32)
	if err != nil || word(got, 0) != 0x5ba00477 {
		t.Fatalf("read: %x %v", got, err)
	}
	if w.taps[1].instruction != 0xfff {
		t.Fatal("other TAP not bypassed")
	}
	if err := conn.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	before := w.calls
	if _, err := tap.ScanDR(ctx, []byte{0}, 1); !errors.Is(err, ErrChainInvalid) {
		t.Fatal(err)
	}
	if w.calls != before {
		t.Fatal("stale TAP sent traffic")
	}
	if err := chain.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tap.ScanIR(ctx, []byte{0xf}); !errors.Is(err, ErrChainInvalid) {
		t.Fatal("old generation revived")
	}
	if err := chain.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if conn.State() != Idle {
		t.Fatal("release did not park idle")
	}
}

func TestLayoutCopyAndInterleaving(t *testing.T) {
	w, _, chain := benchChain(t)
	ctx := context.Background()
	if err := chain.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := chain.TAP(0)
	b, _ := chain.TAP(1)
	if _, err := a.ScanIR(ctx, []byte{0xe}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ScanIR(ctx, []byte{0xe, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ScanDR(ctx, make([]byte, 4), 32); !errors.Is(err, ErrInstructionChanged) {
		t.Fatal(err)
	}
	before := w.calls
	if _, err := b.ScanDR(ctx, []byte{0}, 9); err == nil || w.calls != before {
		t.Fatal("invalid DR sent traffic")
	}
	if _, err := b.ScanIR(ctx, []byte{0xff, 0xff}); err == nil {
		t.Fatal("oversized instruction accepted")
	}
}

func TestSecondTAPReadAndDetachedLayout(t *testing.T) {
	w, conn, _ := benchChain(t)
	a, _ := IDCODE(4, 0x5ba00477)
	b, _ := IDCODE(12, 0x14730093)
	layout := Layout{a, b}
	chain, err := NewChain(conn, layout)
	if err != nil {
		t.Fatal(err)
	}
	layout[0] = TAPSpec{}
	ctx := context.Background()
	if err := chain.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tap, _ := chain.TAP(1)
	if _, err := tap.ScanIR(ctx, []byte{0xe, 0}); err != nil {
		t.Fatal(err)
	}
	got, err := tap.ScanDR(ctx, make([]byte, 4), 32)
	if err != nil || word(got, 0) != 0x14730093 {
		t.Fatalf("second TAP: %x %v", got, err)
	}
	if w.taps[0].instruction != 15 {
		t.Fatal("first TAP not bypassed")
	}
}

func TestFailedReleaseRetainsRetry(t *testing.T) {
	w, _, chain := benchChain(t)
	ctx := context.Background()
	if err := chain.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tap, _ := chain.TAP(0)
	w.fail = w.calls + 2
	if err := chain.Release(ctx); err == nil {
		t.Fatal("missing release error")
	}
	before := w.calls
	if _, err := tap.ScanIR(ctx, []byte{14}); !errors.Is(err, ErrChainInvalid) || w.calls != before {
		t.Fatal("failed release retained a usable borrower")
	}
	w.fail = 0
	if err := chain.Release(ctx); err != nil {
		t.Fatal(err)
	}
	before = w.calls
	if err := chain.Release(ctx); err != nil || w.calls != before {
		t.Fatal("release is not idempotent")
	}
}

func TestWireFailureInvalidatesAllTAPs(t *testing.T) {
	w, _, chain := benchChain(t)
	ctx := context.Background()
	if err := chain.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := chain.TAP(0)
	b, _ := chain.TAP(1)
	w.fail = w.calls + 1
	if _, err := a.ScanIR(ctx, []byte{14}); err == nil {
		t.Fatal("missing error")
	}
	if _, err := b.ScanIR(ctx, []byte{14, 0}); !errors.Is(err, ErrChainInvalid) {
		t.Fatal(err)
	}
	w.fail = 0
	w.taps[0].id = 7
	if err := chain.Release(ctx); err == nil {
		t.Fatal("cleanup accepted replacement identity")
	}
}
