package armdebug_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/probe"
)

type attachment struct{}

func (attachment) Info() discover.AttachmentInfo {
	return discover.AttachmentInfo{Key: "one"}
}

type openFixture struct {
	id                       discover.ProviderID
	enumerations, opens      int
	discoveryErr, openingErr error
	bench                    *bench
}

var fixtureSequence atomic.Uint64

func registerOpenFixture(t *testing.T) *openFixture {
	t.Helper()
	f := &openFixture{bench: newBench(), id: discover.ProviderID(fmt.Sprintf("armdebug-test-%d", fixtureSequence.Add(1)))}
	transport := discover.NewTransportProvider(f.id, func(context.Context) ([]discover.Attachment, error) {
		f.enumerations++
		return []discover.Attachment{attachment{}}, f.discoveryErr
	})
	provider := discover.NewProbeProvider(f.id, f.id, func(context.Context, discover.Transport) ([]discover.Candidate, error) {
		var candidates []discover.Candidate
		for _, serial := range []string{"one", "two"} {
			candidates = append(candidates, discover.NewCandidate(probe.Info{Serial: serial}, serial, func(context.Context) (*probe.Probe, error) {
				f.opens++
				if serial != "one" {
					t.Fatal("opened another binding")
				}
				return probe.New(probe.Info{Serial: serial}, f.bench), f.openingErr
			}))
		}
		return candidates, nil
	})
	if err := discover.RegisterTransport(transport); err != nil {
		t.Fatal(err)
	}
	if err := discover.RegisterProbe(provider); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOpenUsesOneExactDiscoveryAndBinding(t *testing.T) {
	f := registerOpenFixture(t)
	selection := discover.Selection{Provider: f.id, Serial: "one"}
	checkOpenPreflight(t, f, selection)
	before := f.enumerations
	c, err := armdebug.Open(t.Context(), selection, config())
	if err != nil || f.enumerations != before+1 || f.opens != 1 || f.bench.activations != 1 || f.bench.target.resets != 2 {
		t.Fatalf("combined open: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	f.bench = newBench()
	f.openingErr, f.bench.closeErr = errors.New("open"), errors.New("cleanup")
	c, err = armdebug.Open(t.Context(), selection, config())
	if c == nil || !errors.Is(err, f.openingErr) || !errors.Is(err, f.bench.closeErr) || f.opens != 2 || f.bench.activations != 0 {
		t.Fatalf("opening error cleanup: %v", err)
	}
	f.bench.closeErr = nil
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func checkOpenPreflight(t *testing.T, f *openFixture, selection discover.Selection) {
	t.Helper()
	if c, err := armdebug.Open(t.Context(), selection, armdebug.Config{}); c != nil || err == nil || f.enumerations != 0 {
		t.Fatal("invalid config reached discovery")
	}
	if c, err := armdebug.Open(t.Context(), discover.Selection{Provider: f.id}, config()); c != nil || !errors.Is(err, discover.ErrCandidateAmbiguous) || f.opens != 0 {
		t.Fatal("ambiguous selection opened")
	}
	f.discoveryErr = errors.New("partial enumeration")
	if c, err := armdebug.Open(t.Context(), selection, config()); c != nil || !errors.Is(err, f.discoveryErr) || f.opens != 0 {
		t.Fatal("partial discovery opened")
	}
	f.discoveryErr = nil
}

func TestExplicitRegistryThenConnect(t *testing.T) {
	var registry discover.Registry
	b := newBench()
	if err := registry.RegisterTransport(discover.NewTransportProvider("explicit", func(context.Context) ([]discover.Attachment, error) {
		return []discover.Attachment{attachment{}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterProbe(discover.NewProbeProvider("explicit", "explicit", func(context.Context, discover.Transport) ([]discover.Candidate, error) {
		return []discover.Candidate{discover.NewCandidate(probe.Info{}, "one", func(context.Context) (*probe.Probe, error) {
			return probe.New(probe.Info{}, b), nil
		})}, nil
	})); err != nil {
		t.Fatal(err)
	}
	opened, err := registry.OpenProbe(t.Context(), discover.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := armdebug.Connect(t.Context(), opened, config())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
