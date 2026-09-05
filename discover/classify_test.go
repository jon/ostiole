package discover

import (
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	"github.com/jon/ostiole/probe"
)

func TestClassificationUsesTransportSnapshot(t *testing.T) {
	var r Registry
	enumerations, classifications := 0, 0
	transport := NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
		enumerations++
		return []Attachment{testAttachment{serial: "same", key: "1"}}, nil
	})
	if err := r.RegisterTransport(transport); err != nil {
		t.Fatal(err)
	}
	makeProvider := func(id ProviderID) *ProbeProvider {
		return NewProbeProvider(id, "usb", func(_ context.Context, a Transport) ([]Candidate, error) {
			classifications++
			return []Candidate{NewCandidate(probe.Info{Serial: a.Info().Serial}, "one", func(context.Context) (*probe.Probe, error) {
				t.Fatal("classification opened hardware")
				return nil, nil
			})}, nil
		})
	}
	for _, id := range []ProviderID{"z", "a", "m"} {
		if err := r.RegisterProbe(makeProvider(id)); err != nil {
			t.Fatal(err)
		}
	}
	i, err := r.Transports(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(makeProvider("later")); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(makeProvider("a")); !errors.Is(err, ErrDuplicateProvider) {
		t.Fatal(err)
	}
	probes, err := i.Probes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := slices.Collect(iter.Seq[Candidate](probes))
	if len(got) != 3 || got[0].Info().Provider != "a" || got[2].Info().Provider != "z" {
		t.Fatalf("order: %v", got)
	}
	for range probes {
		break
	}
	if enumerations != 1 || classifications != 3 {
		t.Fatalf("calls: %d, %d", enumerations, classifications)
	}
}

func TestClassificationKeepsPartialResults(t *testing.T) {
	var r Registry
	want := errors.New("classify")
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
		return []Attachment{testAttachment{key: "1"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(NewProbeProvider("probe", "usb", func(context.Context, Transport) ([]Candidate, error) {
		return []Candidate{NewCandidate(probe.Info{}, "1", func(context.Context) (*probe.Probe, error) { return nil, want })}, want
	})); err != nil {
		t.Fatal(err)
	}
	i, err := r.Transports(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p, err := i.Probes(t.Context())
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := p.Select(Selection{}); err != nil {
		t.Fatal(err)
	}
	var empty TransportInventory
	p, err = empty.Probes(t.Context())
	if err != nil || p == nil {
		t.Fatal("nil inventory not empty")
	}
}
