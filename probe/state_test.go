package probe

import (
	"errors"
	"testing"
)

type stateBackend struct {
	backend
	pending, possible bool
	release           bool
}

func (b *stateBackend) CleanupPending() bool      { return b.pending }
func (b *stateBackend) RestorationPossible() bool { return b.possible }
func (b *stateBackend) Close() error {
	if b.release {
		b.pending = false
	}
	return b.backend.Close()
}

func TestProbeFinalBackendErrorIsNotRetried(t *testing.T) {
	want := errors.New("final close error")
	b := &stateBackend{backend: backend{closeErr: want}, pending: true, release: true}
	p := New(Info{}, b)
	if err := p.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close() = %v", err)
	}
	if err := p.Close(); err != nil || b.closes != 1 {
		t.Fatalf("repeated Close() = %v; backend calls=%d", err, b.closes)
	}
}

func TestProbeStateQueriesKeepLegacyFailuresConservative(t *testing.T) {
	for _, p := range []*Probe{nil, {}, New(Info{}, nil)} {
		if p.CleanupPending() || p.RestorationPossible() {
			t.Fatal("empty owner reported resources or restoration")
		}
	}
	b := &backend{closeErr: errors.New("live close failure")}
	p := New(Info{}, b)
	if !p.CleanupPending() || !p.RestorationPossible() || b.closes != 0 || b.opens != 0 || b.calls != 0 {
		t.Fatal("legacy queries were not conservative or performed traffic")
	}
	if err := p.Close(); !errors.Is(err, b.closeErr) || !p.CleanupPending() || !p.RestorationPossible() {
		t.Fatalf("live failure lost ownership: %v", err)
	}
	b.closeErr = nil
	if err := p.Close(); err != nil || p.CleanupPending() || p.RestorationPossible() || b.closes != 2 {
		t.Fatalf("successful retry = %v, closes=%d", err, b.closes)
	}
}

func TestProbeOptionalStateAndRetainedClose(t *testing.T) {
	want := errors.New("live cleanup failure")
	b := &stateBackend{backend: backend{closeErr: want}, pending: true, possible: true}
	p := New(Info{}, b)
	if !p.CleanupPending() || !p.RestorationPossible() {
		t.Fatal("optional state ignored")
	}
	b.possible = false
	if p.RestorationPossible() || !p.CleanupPending() || b.closes != 0 {
		t.Fatal("lost restoration was confused with host release")
	}
	if err := p.Close(); !errors.Is(err, want) || !p.CleanupPending() {
		t.Fatalf("retained cleanup = %v", err)
	}
	b.release = true
	if err := p.Close(); !errors.Is(err, want) || p.CleanupPending() || p.RestorationPossible() {
		t.Fatalf("final cleanup = %v", err)
	}
	if err := p.Close(); err != nil || b.closes != 2 {
		t.Fatalf("released backend retried: %v, calls=%d", err, b.closes)
	}
}

type restorationOnlyBackend struct{ backend }

func (*restorationOnlyBackend) RestorationPossible() bool { return false }

type cleanupOnlyBackend struct{ backend }

func (*cleanupOnlyBackend) CleanupPending() bool { return false }

func TestProbeStateInterfacesAreIndependent(t *testing.T) {
	p := New(Info{}, &restorationOnlyBackend{})
	if p.RestorationPossible() || !p.CleanupPending() {
		t.Fatal("restoration state altered conservative cleanup state")
	}
	p = New(Info{}, &cleanupOnlyBackend{})
	if !p.RestorationPossible() || p.CleanupPending() {
		t.Fatal("cleanup state altered conservative restoration state")
	}
}
