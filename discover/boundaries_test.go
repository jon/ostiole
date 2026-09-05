package discover

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jon/ostiole/probe"
)

func TestGlobalConveniencePaths(t *testing.T) {
	id := ProviderID(t.Name())
	t.Cleanup(func() {
		defaultRegistry.mu.Lock()
		defer defaultRegistry.mu.Unlock()
		delete(defaultRegistry.transports, id)
		delete(defaultRegistry.probes, probeKey{id, id})
	})
	opens, enumerations := 0, 0
	owner := probe.New(probe.Info{}, nil)
	transport := NewTransportProvider(id, func(context.Context) ([]Attachment, error) {
		enumerations++
		return []Attachment{testAttachment{key: "one"}}, nil
	})
	if err := RegisterTransport(transport); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTransport(transport); err != nil {
		t.Fatal(err)
	}
	if err := RegisterProbe(NewProbeProvider(id, id, func(context.Context, Transport) ([]Candidate, error) {
		return []Candidate{NewCandidate(probe.Info{Serial: "exact"}, "one", func(context.Context) (*probe.Probe, error) {
			opens++
			return owner, nil
		})}, nil
	})); err != nil {
		t.Fatal(err)
	}
	selected := Selection{Provider: id, Serial: "exact"}
	got, err := OpenProbe(t.Context(), selected)
	if err != nil || got != owner {
		t.Fatalf("combined: %v", err)
	}
	i, err := Probes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err = i.Open(t.Context(), selected)
	if err != nil || got != owner {
		t.Fatalf("inventory: %v", err)
	}
	ts, err := Transports(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	i, err = ts.Probes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, err := i.Select(selected)
	if err != nil {
		t.Fatal(err)
	}
	got, err = c.Open(t.Context())
	if err != nil || got != owner || opens != 3 || enumerations != 3 {
		t.Fatalf("explicit: %v, %d opens, %d enumerations", err, opens, enumerations)
	}
}

func TestRegistrationValidation(t *testing.T) {
	var r Registry
	for _, p := range []*TransportProvider{nil, {}, NewTransportProvider("", func(context.Context) ([]Attachment, error) { return nil, nil })} {
		if err := r.RegisterTransport(p); err == nil {
			t.Fatal("invalid transport registered")
		}
	}
	for _, p := range []*ProbeProvider{nil, {}, NewProbeProvider("p", "", func(context.Context, Transport) ([]Candidate, error) { return nil, nil })} {
		if err := r.RegisterProbe(p); err == nil {
			t.Fatal("invalid probe registered")
		}
	}
	for _, transport := range []ProviderID{"usb", "network"} {
		if err := r.RegisterProbe(NewProbeProvider("same", transport, func(context.Context, Transport) ([]Candidate, error) { return nil, nil })); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscoveryCancellationStopsIndependentProviders(t *testing.T) {
	var r Registry
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for _, id := range []ProviderID{"a", "b"} {
		if err := r.RegisterTransport(NewTransportProvider(id, func(context.Context) ([]Attachment, error) {
			if id == "b" {
				t.Fatal("scheduled after cancellation")
			}
			cancel()
			return []Attachment{testAttachment{key: "one"}}, nil
		})); err != nil {
			t.Fatal(err)
		}
	}
	i, err := r.Transports(ctx)
	if !errors.Is(err, context.Canceled) || len(slices.Collect(iter.Seq[Transport](i))) != 1 {
		t.Fatalf("partial cancellation: %v", err)
	}
}

func TestClassificationCancellationAndAttribution(t *testing.T) {
	var r Registry
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
		return []Attachment{testAttachment{key: "one"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	want := errors.New("primary")
	for _, id := range []ProviderID{"a", "b"} {
		if err := r.RegisterProbe(NewProbeProvider(id, "usb", func(context.Context, Transport) ([]Candidate, error) {
			if id == "b" {
				t.Fatal("classified after cancellation")
			}
			cancel()
			return nil, want
		})); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.Probes(ctx)
	if !errors.Is(err, want) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "probe a over usb at one") {
		t.Fatalf("attribution: %v", err)
	}
}

func TestConcurrentRegistrationAndEnumeration(t *testing.T) {
	var r Registry
	var wg sync.WaitGroup
	for n := range 12 {
		wg.Go(func() {
			p := NewTransportProvider(ProviderID(fmt.Sprint(n)), func(context.Context) ([]Attachment, error) { return nil, nil })
			if err := r.RegisterTransport(p); err != nil {
				t.Error(err)
			}
			if _, err := r.Transports(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
