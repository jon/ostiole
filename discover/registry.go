// Package discover enumerates registered providers and returns detached inventories.
package discover

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// ProviderID identifies a provider; provider packages define their constants.
type ProviderID string

var (
	// ErrDuplicateProvider reports an already registered provider ID.
	ErrDuplicateProvider = errors.New("discover: duplicate provider")
	// ErrNoProviders distinguishes absent registrations from absent hardware.
	ErrNoProviders = errors.New("discover: no providers registered")
)

// TransportProvider describes an immutable transport enumerator. Attachments
// returned by its callback must be detached, immutable, and non-nil.
type TransportProvider struct {
	id        ProviderID
	enumerate func(context.Context) ([]Attachment, error)
}

// NewTransportProvider describes enumeration without running it. Registration
// rejects empty IDs and nil callbacks. Keep this value for dependency reuse.
func NewTransportProvider(id ProviderID, enumerate func(context.Context) ([]Attachment, error)) *TransportProvider {
	return &TransportProvider{id: id, enumerate: enumerate}
}

// Registry holds providers. Its zero value is ready for use. Registration and
// discovery may run concurrently; do not copy a Registry after first use.
type Registry struct {
	mu         sync.Mutex
	transports map[ProviderID]*TransportProvider
}

var defaultRegistry Registry

// RegisterTransport adds a provider to the package registry.
func RegisterTransport(p *TransportProvider) error { return defaultRegistry.RegisterTransport(p) }

// EnsureTransport installs a shared dependency in the package registry.
func EnsureTransport(p *TransportProvider) error { return defaultRegistry.EnsureTransport(p) }

// RegisterTransport rejects any duplicate ID, including the same provider.
func (r *Registry) RegisterTransport(p *TransportProvider) error { return r.addTransport(p, false) }

// EnsureTransport accepts an existing registration only for this exact
// immutable provider value. It never replaces another provider with the ID.
func (r *Registry) EnsureTransport(p *TransportProvider) error { return r.addTransport(p, true) }

func (r *Registry) addTransport(p *TransportProvider, ensure bool) error {
	if p == nil || p.id == "" || p.enumerate == nil {
		return errors.New("discover: invalid transport provider")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous := r.transports[p.id]; previous != nil {
		if ensure && previous == p {
			return nil
		}
		return fmt.Errorf("%w: transport %s", ErrDuplicateProvider, p.id)
	}
	if r.transports == nil {
		r.transports = make(map[ProviderID]*TransportProvider)
	}
	r.transports[p.id] = p
	return nil
}

func (r *Registry) transportSnapshot() []*TransportProvider {
	r.mu.Lock()
	defer r.mu.Unlock()
	var providers []*TransportProvider
	for _, p := range r.transports {
		providers = append(providers, p)
	}
	slices.SortFunc(providers, func(a, b *TransportProvider) int { return compareStrings(string(a.id), string(b.id)) })
	return providers
}
