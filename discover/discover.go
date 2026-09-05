package discover

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jon/ostiole/probe"
)

// Probes enumerates and classifies one package-registry snapshot.
func Probes(ctx context.Context) (ProbeInventory, error) { return defaultRegistry.Probes(ctx) }

// Probes returns sorted candidates alongside transport and classification
// errors. Successful empty results are callable sequences. If no probe providers
// are registered, Probes returns ErrNoProviders without enumerating transports.
func (r *Registry) Probes(ctx context.Context) (ProbeInventory, error) {
	if ctx == nil {
		return nil, errors.New("discover: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transports, probes := r.snapshot()
	if len(probes) == 0 {
		return ProbeInventory(slices.Values([]Candidate{})), ErrNoProviders
	}
	inventory, transportErr := enumerate(ctx, transports, probes)
	candidates, classifyErr := inventory.Probes(ctx)
	return candidates, errors.Join(transportErr, classifyErr)
}

// OpenProbe discovers, selects, and opens one probe using the package registry.
// Any discovery error prevents automatic selection and opening.
func OpenProbe(ctx context.Context, selection Selection) (*probe.Probe, error) {
	return defaultRegistry.OpenProbe(ctx, selection)
}

// OpenProbe calls Probes and then inventory.Open, stopping on discovery errors.
// An owner returned alongside an opening error still requires cleanup.
func (r *Registry) OpenProbe(ctx context.Context, selection Selection) (*probe.Probe, error) {
	inventory, err := r.Probes(ctx)
	if err != nil {
		return nil, err
	}
	return inventory.Open(ctx, selection)
}

func missingDependencies(transports []*TransportProvider, probes []*ProbeProvider) []error {
	var failures []error
	for _, p := range probes {
		if !slices.ContainsFunc(transports, func(t *TransportProvider) bool { return t.id == p.transport }) {
			failures = append(failures, fmt.Errorf("discover: probe %s requires unregistered transport %s", p.id, p.transport))
		}
	}
	return failures
}
