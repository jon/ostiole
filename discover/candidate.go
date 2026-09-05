package discover

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jon/ostiole/probe"
)

// CandidateInfo contains detached display and selection metadata.
type CandidateInfo struct {
	Provider ProviderID
	probe.Info
}

// Candidate describes one unopened binding. Its zero value cannot open.
type Candidate struct {
	info CandidateInfo
	key  string
	open func(context.Context) (*probe.Probe, error)
}

// NewCandidate captures one exact binding without opening it. Key must
// distinguish bindings with equal metadata within one provider. The callback
// must revalidate the attachment and retain failed cleanup in a returned owner.
// Registration supplies Provider; info is copied. Empty keys or nil callbacks
// produce invalid candidates which discovery and Open reject.
func NewCandidate(info probe.Info, key string, open func(context.Context) (*probe.Probe, error)) Candidate {
	return Candidate{info: CandidateInfo{Info: info}, key: key, open: open}
}

// Info returns detached display and selection metadata.
func (c Candidate) Info() CandidateInfo { return c.info }

// Open opens this binding once without rediscovery. If it returns an owner
// with an error, the caller must retain that owner and retry Close as needed.
func (c Candidate) Open(ctx context.Context) (*probe.Probe, error) {
	if ctx == nil {
		return nil, errors.New("discover: nil open context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.open == nil || c.key == "" {
		return nil, errors.New("discover: invalid candidate")
	}
	p, err := c.open(ctx)
	if p == nil && err == nil {
		err = errors.New("discover: opener returned no probe")
	}
	return p, err
}

// ProbeInventory is a repeatable snapshot sequence. Select and Open treat a
// nil sequence as empty; directly ranging nil follows Go's function semantics.
type ProbeInventory func(func(Candidate) bool)

// Selection applies exact filters. Every empty field is a wildcard; the
// complete selection must match exactly one candidate.
type Selection struct {
	Provider                   ProviderID
	Serial, Function, Location string
}

var (
	// ErrCandidateNotFound reports no matching candidate.
	ErrCandidateNotFound = errors.New("discover: candidate not found")
	// ErrCandidateAmbiguous reports more than one matching candidate.
	ErrCandidateAmbiguous = errors.New("discover: candidate selection is ambiguous")
)

// SelectionError preserves the error category and detached matching metadata.
type SelectionError struct {
	category   error
	candidates []CandidateInfo
}

// Error describes the selection failure.
func (e *SelectionError) Error() string {
	return fmt.Sprintf("%v: %d matches", e.category, len(e.candidates))
}

// Unwrap exposes the selection category.
func (e *SelectionError) Unwrap() error { return e.category }

// Candidates returns a copy of matching metadata in inventory order.
func (e *SelectionError) Candidates() []CandidateInfo { return slices.Clone(e.candidates) }

// Select requires exactly one match and performs no I/O.
func (i ProbeInventory) Select(selection Selection) (Candidate, error) {
	var found []CandidateInfo
	var selected Candidate
	if i != nil {
		for c := range i {
			if selection.matches(c.info) {
				selected = c
				found = append(found, c.info)
			}
		}
	}
	if len(found) == 1 {
		return selected, nil
	}
	category := ErrCandidateAmbiguous
	if len(found) == 0 {
		category = ErrCandidateNotFound
	}
	return Candidate{}, &SelectionError{category: category, candidates: found}
}

// Open selects exactly one candidate, then opens that binding once.
func (i ProbeInventory) Open(ctx context.Context, selection Selection) (*probe.Probe, error) {
	candidate, err := i.Select(selection)
	if err != nil {
		return nil, err
	}
	return candidate.Open(ctx)
}

func (s Selection) matches(i CandidateInfo) bool {
	return (s.Provider == "" || s.Provider == i.Provider) &&
		(s.Serial == "" || s.Serial == i.Serial) &&
		(s.Function == "" || s.Function == i.Function) &&
		(s.Location == "" || s.Location == i.Location)
}
