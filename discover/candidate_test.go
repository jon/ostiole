package discover

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/probe"
)

func TestSelectAndOpenShareExactCandidate(t *testing.T) {
	opens := 0
	want := probe.New(probe.Info{}, nil)
	a := NewCandidate(probe.Info{Serial: "same", Function: "A"}, "a", func(context.Context) (*probe.Probe, error) { opens++; return want, nil })
	b := NewCandidate(probe.Info{Serial: "same", Function: "B"}, "b", func(context.Context) (*probe.Probe, error) { t.Fatal("wrong open"); return nil, nil })
	i := ProbeInventory(func(yield func(Candidate) bool) {
		if yield(a) {
			yield(b)
		}
	})
	if _, err := i.Select(Selection{Serial: "same"}); !errors.Is(err, ErrCandidateAmbiguous) {
		t.Fatalf("ambiguity: %v", err)
	}
	if _, err := i.Select(Selection{Serial: "missing"}); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if opens != 0 {
		t.Fatal("selection opened")
	}
	got, err := i.Open(t.Context(), Selection{Function: "A"})
	if err != nil || got != want || opens != 1 {
		t.Fatalf("open: %v", err)
	}
	var zero ProbeInventory
	if _, err := zero.Open(t.Context(), Selection{}); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatal("nil inventory")
	}
}

func TestCandidateOpenFailureAndPreflight(t *testing.T) {
	calls := 0
	want := errors.New("open")
	owner := probe.New(probe.Info{}, nil)
	c := NewCandidate(probe.Info{}, "one", func(context.Context) (*probe.Probe, error) { calls++; return owner, want })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Open(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation")
	}
	if calls != 0 {
		t.Fatal("opened canceled candidate")
	}
	got, err := c.Open(t.Context())
	if got != owner || !errors.Is(err, want) || calls != 1 {
		t.Fatal("lost cleanup owner")
	}
	if _, err := (Candidate{}).Open(t.Context()); err == nil {
		t.Fatal("zero opened")
	}
}
