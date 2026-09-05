package discover

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
)

// AttachmentInfo describes one attachment. Key must uniquely identify it
// within its transport provider and give a deterministic final sort key.
type AttachmentInfo struct{ Product, Serial, Location, Key string }

// Attachment is detached transport-specific identity. Implementations must
// remain immutable and must not retain an open resource or typed nil value.
type Attachment interface{ Info() AttachmentInfo }

// TransportInfo adds the enumerating provider to attachment metadata.
type TransportInfo struct {
	Provider ProviderID
	AttachmentInfo
}

// Transport is a detached attachment from a registry snapshot.
type Transport struct {
	info        TransportInfo
	attachment  Attachment
	classifiers []*ProbeProvider
}

// Info returns detached metadata.
func (t Transport) Info() TransportInfo { return t.info }

// Attachment returns the immutable transport-specific identity for classifiers.
func (t Transport) Attachment() Attachment { return t.attachment }

// TransportInventory is a repeatable snapshot sequence; iteration performs no I/O.
type TransportInventory func(func(Transport) bool)

// Transports enumerates each package-registered transport once.
func Transports(ctx context.Context) (TransportInventory, error) {
	return defaultRegistry.Transports(ctx)
}

// Transports returns sorted successful attachments alongside attributed errors.
// Handle those errors before using a partial inventory. Ordering is provider,
// serial, location, product, then key, all case-sensitive and lexicographic.
func (r *Registry) Transports(ctx context.Context) (TransportInventory, error) {
	providers, classifiers := r.snapshot()
	return enumerate(ctx, providers, classifiers)
}

func enumerate(ctx context.Context, providers []*TransportProvider, classifiers []*ProbeProvider) (TransportInventory, error) {
	if ctx == nil {
		return nil, errors.New("discover: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return TransportInventory(slices.Values([]Transport{})), ErrNoProviders
	}
	var found []Transport
	failures := missingDependencies(providers, classifiers)
	for _, p := range providers {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		attachments, err := p.enumerate(ctx)
		if err != nil {
			failures = append(failures, fmt.Errorf("discover: transport %s: %w", p.id, err))
		}
		for _, a := range attachments {
			if a == nil || a.Info().Key == "" {
				failures = append(failures, fmt.Errorf("discover: transport %s returned invalid attachment", p.id))
				continue
			}
			found = append(found, Transport{info: TransportInfo{Provider: p.id, AttachmentInfo: a.Info()}, attachment: a, classifiers: classifiers})
		}
	}
	slices.SortFunc(found, compareTransports)
	return TransportInventory(slices.Values(found)), errors.Join(append(failures, ctx.Err())...)
}

func compareStrings(a, b string) int { return cmp.Compare(a, b) }

func compareTransports(a, b Transport) int {
	x, y := a.info, b.info
	return cmp.Or(cmp.Compare(x.Provider, y.Provider), cmp.Compare(x.Serial, y.Serial),
		cmp.Compare(x.Location, y.Location), cmp.Compare(x.Product, y.Product), cmp.Compare(x.Key, y.Key))
}
