package discover

import (
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	"github.com/jon/ostiole/probe"
)

func bindingRegistry(t *testing.T) (*Registry, map[string]int) {
	t.Helper()
	var r Registry
	opens := make(map[string]int)
	for _, transport := range []ProviderID{"usb", "tcp"} {
		if err := r.RegisterTransport(NewTransportProvider(transport, func(context.Context) ([]Attachment, error) {
			return []Attachment{testAttachment{key: "attachment"}}, nil
		})); err != nil {
			t.Fatal(err)
		}
		if err := r.RegisterProbe(NewProbeProvider("driver", transport, func(context.Context, Transport) ([]Candidate, error) {
			var candidates []Candidate
			for _, key := range []string{"a", "b"} {
				candidates = append(candidates, NewCandidate(probe.Info{}, key, func(context.Context) (*probe.Probe, error) {
					opens[string(transport)+key]++
					return probe.New(probe.Info{}, nil), nil
				}))
			}
			return candidates, nil
		})); err != nil {
			t.Fatal(err)
		}
	}
	return &r, opens
}

func TestBindingSelectionDisambiguatesIdenticalMetadata(t *testing.T) {
	r, opens := bindingRegistry(t)
	inventory, err := r.Probes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = inventory.Select(Selection{})
	var ambiguity *SelectionError
	if !errors.As(err, &ambiguity) || !errors.Is(err, ErrCandidateAmbiguous) {
		t.Fatalf("ambiguity: %v", err)
	}
	seen := make(map[BindingID]bool)
	for index, c := range slices.Collect(iter.Seq[Candidate](inventory)) {
		id := c.Info().Binding
		if id == "" || seen[id] || ambiguity.Candidates()[index].Binding != id {
			t.Fatalf("missing or colliding binding: %q", id)
		}
		seen[id] = true
		selection := Selection{Binding: id}
		if _, err := inventory.Select(Selection{Binding: id, Serial: "other"}); !errors.Is(err, ErrCandidateNotFound) {
			t.Fatalf("binding ignored serial filter: %v", err)
		}
		selected, err := inventory.Select(selection)
		if err != nil || selected.Info().Binding != id {
			t.Fatalf("select binding: %v", err)
		}
		owner, err := r.OpenProbe(t.Context(), selection)
		if err != nil {
			t.Fatal(err)
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(opens) != 4 {
		t.Fatalf("opened bindings: %v", opens)
	}
	for key, count := range opens {
		if count != 1 {
			t.Fatalf("%s opened %d times", key, count)
		}
	}
	if _, err := inventory.Open(t.Context(), Selection{Binding: "unknown"}); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("unknown binding: %v", err)
	}
}

func TestBindingIdentityNamespacesAndZero(t *testing.T) {
	if (Candidate{}).Info().Binding != "" {
		t.Fatal("zero candidate has a binding")
	}
	seen := make(map[BindingID]bool)
	for _, c := range []Candidate{
		{info: CandidateInfo{Provider: "a/b"}, transport: "c", key: "d"},
		{info: CandidateInfo{Provider: "a"}, transport: "b/c", key: "d"},
		{info: CandidateInfo{Provider: "a"}, transport: "b", key: "c/d"},
		{info: CandidateInfo{Provider: "other"}, transport: "b", key: "c/d"},
	} {
		id := c.Info().Binding
		if seen[id] || c.Info().Binding != id {
			t.Fatalf("colliding or unstable binding %q", id)
		}
		seen[id] = true
	}
}
