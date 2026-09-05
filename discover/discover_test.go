package discover

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/probe"
)

func TestCombinedDiscoveryDoesNotOpenPartialInventory(t *testing.T) {
	var r Registry
	enumerations, opens := 0, 0
	want := errors.New("enumeration incomplete")
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
		enumerations++
		return []Attachment{testAttachment{key: "1"}}, want
	})); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(NewProbeProvider("probe", "usb", func(context.Context, Transport) ([]Candidate, error) {
		return []Candidate{NewCandidate(probe.Info{Serial: "chosen"}, "1", func(context.Context) (*probe.Probe, error) {
			opens++
			return probe.New(probe.Info{}, nil), nil
		})}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if p, err := r.OpenProbe(t.Context(), Selection{}); p != nil || !errors.Is(err, want) {
		t.Fatalf("open: %v", err)
	}
	if enumerations != 1 || opens != 0 {
		t.Fatal("opened partial discovery")
	}
	i, err := r.Probes(t.Context())
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := i.Open(t.Context(), Selection{Serial: "chosen"}); err != nil {
		t.Fatal(err)
	}
	if enumerations != 2 || opens != 1 {
		t.Fatal("rediscovery or fallback")
	}
}

func TestCombinedDiscoveryRequiresProvidersAndDependencies(t *testing.T) {
	var r Registry
	if _, err := r.Probes(t.Context()); !errors.Is(err, ErrNoProviders) {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(NewProbeProvider("missing", "absent", func(context.Context, Transport) ([]Candidate, error) {
		t.Fatal("missing dependency classified")
		return nil, nil
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Probes(t.Context()); err == nil {
		t.Fatal("missing dependency accepted")
	}
}
