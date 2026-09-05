package discover

import (
	"context"
	"errors"
	"iter"
	"slices"
	"sync"
	"testing"
)

type testAttachment struct{ serial, key string }

func (a testAttachment) Info() AttachmentInfo { return AttachmentInfo{Serial: a.serial, Key: a.key} }

func TestTransportRegistrationAndSnapshot(t *testing.T) {
	var r Registry
	if _, err := r.Transports(t.Context()); !errors.Is(err, ErrNoProviders) {
		t.Fatalf("empty registry: %v", err)
	}
	calls := 0
	p := NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
		calls++
		return []Attachment{testAttachment{"z", "2"}, testAttachment{"a", "1"}}, nil
	})
	if err := r.RegisterTransport(p); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.RegisterTransport(p), ErrDuplicateProvider) {
		t.Fatal("duplicate accepted")
	}
	if err := r.EnsureTransport(p); err != nil {
		t.Fatal(err)
	}
	other := NewTransportProvider("usb", func(context.Context) ([]Attachment, error) { return nil, nil })
	if !errors.Is(r.EnsureTransport(other), ErrDuplicateProvider) {
		t.Fatal("different dependency replaced")
	}
	inv, err := r.Transports(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got := slices.Collect(iter.Seq[Transport](inv))
		if len(got) != 2 || got[0].Info().Serial != "a" || got[1].Info().Serial != "z" {
			t.Fatalf("order: %v", got)
		}
	}
	for range inv {
		break
	}
	if calls != 1 {
		t.Fatal("iteration enumerated hardware")
	}
}

func TestTransportPartialFailureAndCancellation(t *testing.T) {
	var r Registry
	want := errors.New("enumeration")
	if err := r.RegisterTransport(NewTransportProvider("a", func(context.Context) ([]Attachment, error) {
		return []Attachment{testAttachment{"one", "1"}}, want
	})); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterTransport(NewTransportProvider("b", func(context.Context) ([]Attachment, error) {
		return []Attachment{testAttachment{"two", "2"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	inv, err := r.Transports(t.Context())
	if !errors.Is(err, want) || len(slices.Collect(iter.Seq[Transport](inv))) != 2 {
		t.Fatalf("partial result: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Transports(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation")
	}
}

func TestRegistryConcurrentEnsure(t *testing.T) {
	var r Registry
	p := NewTransportProvider("test", func(context.Context) ([]Attachment, error) { return nil, nil })
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := r.EnsureTransport(p); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	inv, err := r.Transports(t.Context())
	if err != nil || inv == nil || len(slices.Collect(iter.Seq[Transport](inv))) != 0 {
		t.Fatalf("empty success: %v", err)
	}
}
