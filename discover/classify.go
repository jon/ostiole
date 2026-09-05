package discover

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
)

type probeKey struct{ id, transport ProviderID }

// ProbeProvider classifies attachments from one required transport provider.
// A provider ID may register separate bindings for different transports.
type ProbeProvider struct {
	probeKey
	classify func(context.Context, Transport) ([]Candidate, error)
}

// NewProbeProvider describes classification without performing hardware I/O.
// Registration rejects empty IDs and nil callbacks. Classification must not
// open hardware; candidates capture the exact attachment and driver binding.
func NewProbeProvider(id, transport ProviderID, classify func(context.Context, Transport) ([]Candidate, error)) *ProbeProvider {
	return &ProbeProvider{probeKey: probeKey{id, transport}, classify: classify}
}

// RegisterProbe adds a probe binding to the package registry.
func RegisterProbe(p *ProbeProvider) error { return defaultRegistry.RegisterProbe(p) }

// RegisterProbe rejects duplicate provider/transport pairs. Dependencies may
// be installed before or after registration, but must exist for discovery.
func (r *Registry) RegisterProbe(p *ProbeProvider) error {
	if p == nil || p.id == "" || p.transport == "" || p.classify == nil {
		return errors.New("discover: invalid probe provider")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probes[p.probeKey] != nil {
		return fmt.Errorf("%w: probe %s over %s", ErrDuplicateProvider, p.id, p.transport)
	}
	if r.probes == nil {
		r.probes = make(map[probeKey]*ProbeProvider)
	}
	r.probes[p.probeKey] = p
	return nil
}

type classification struct {
	provider  *ProbeProvider
	transport Transport
}

// Probes classifies the snapshot using the registrations captured by Transports.
// It performs no enumeration. Handle Transports' error separately: a sequence
// does not carry that error, including when its snapshot is empty. Nil is empty.
// Results are ordered by provider, serial, function, location, product, then
// binding key, using case-sensitive lexical comparisons with empty strings first.
func (i TransportInventory) Probes(ctx context.Context) (ProbeInventory, error) {
	if ctx == nil {
		return nil, errors.New("discover: nil context")
	}
	work := i.classificationWork()
	var found []Candidate
	var failures []error
	for _, w := range work {
		if ctx.Err() != nil {
			break
		}
		candidates, err := w.provider.classify(ctx, w.transport)
		if err != nil {
			failures = append(failures, fmt.Errorf("discover: probe %s over %s at %s: %w",
				w.provider.id, w.provider.transport, w.transport.info.Key, err))
		}
		for _, c := range candidates {
			if c.open == nil || c.key == "" {
				failures = append(failures, fmt.Errorf("discover: probe %s returned invalid candidate", w.provider.id))
				continue
			}
			c.info.Provider = w.provider.id
			c.transport = w.provider.transport
			found = append(found, c)
		}
	}
	slices.SortFunc(found, compareCandidates)
	return ProbeInventory(slices.Values(found)), errors.Join(append(failures, ctx.Err())...)
}

func (i TransportInventory) classificationWork() []classification {
	var work []classification
	if i != nil {
		for t := range i {
			for _, p := range t.classifiers {
				if p.transport == t.info.Provider {
					work = append(work, classification{p, t})
				}
			}
		}
	}
	slices.SortFunc(work, func(a, b classification) int {
		return cmp.Or(cmp.Compare(a.provider.id, b.provider.id), compareTransports(a.transport, b.transport))
	})
	return work
}

func compareCandidates(a, b Candidate) int {
	x, y := a.info, b.info
	return cmp.Or(cmp.Compare(x.Provider, y.Provider), cmp.Compare(x.Serial, y.Serial),
		cmp.Compare(x.Function, y.Function), cmp.Compare(x.Location, y.Location),
		cmp.Compare(x.Product, y.Product), cmp.Compare(a.transport, b.transport), cmp.Compare(a.key, b.key))
}
