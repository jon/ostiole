package discover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jon/ostiole/probe"
)

func TestCapturedInventoryReportsMissingProbeProviders(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "attachment"}[present], func(t *testing.T) {
			var r Registry
			if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
				if present {
					return []Attachment{testAttachment{key: "selected"}}, nil
				}
				return nil, nil
			})); err != nil {
				t.Fatal(err)
			}
			inventory, err := r.Transports(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inventory.Probes(t.Context()); !errors.Is(err, ErrNoProviders) {
				t.Fatalf("missing captured probe providers: %v", err)
			}
		})
	}
}

func TestMissingTransportDependencyRemainsAttributed(t *testing.T) {
	var r Registry
	if err := r.RegisterProbe(NewProbeProvider("classifier", "missing-transport", func(context.Context, Transport) ([]Candidate, error) {
		t.Fatal("classified missing transport")
		return nil, nil
	})); err != nil {
		t.Fatal(err)
	}
	for _, enumerate := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := r.Transports(ctx); return err },
		func(ctx context.Context) error { _, err := r.Probes(ctx); return err },
	} {
		err := enumerate(t.Context())
		if err == nil || !strings.Contains(err.Error(), "classifier requires unregistered transport missing-transport") {
			t.Errorf("lost dependency attribution: %v", err)
		}
	}
}

func TestDiscoveryReportsCancellationOnce(t *testing.T) {
	var r Registry
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.RegisterTransport(NewTransportProvider("a", func(context.Context) ([]Attachment, error) { cancel(); return nil, ctx.Err() })); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterTransport(NewTransportProvider("b", func(context.Context) ([]Attachment, error) { t.Fatal("enumerated after cancellation"); return nil, nil })); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(NewProbeProvider("classifier", "a", func(context.Context, Transport) ([]Candidate, error) {
		t.Fatal("classified after cancellation")
		return nil, nil
	})); err != nil {
		t.Fatal(err)
	}
	_, err := r.Probes(ctx)
	if !errors.Is(err, context.Canceled) || strings.Count(err.Error(), context.Canceled.Error()) != 1 {
		t.Fatalf("cancellation should appear once: %v", err)
	}
}

func TestEmptyInventoryRetainsCapturedProviders(t *testing.T) {
	var r Registry
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) { return nil, nil })); err != nil {
		t.Fatal(err)
	}
	before, err := r.Transports(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(NewProbeProvider("classifier", "usb", func(context.Context, Transport) ([]Candidate, error) {
		t.Fatal("classified absent attachment")
		return nil, nil
	})); err != nil {
		t.Fatal(err)
	}
	after, err := r.Transports(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.Probes(t.Context()); !errors.Is(err, ErrNoProviders) {
		t.Fatalf("snapshot used later registration: %v", err)
	}
	candidates, err := after.Probes(t.Context())
	if err != nil || candidates == nil {
		t.Fatalf("registered empty snapshot: %v", err)
	}
	for range after.All() {
		t.Fatal("empty snapshot fabricated an attachment")
	}
}

func TestInvalidProviderOutputKeepsAttributionAndValidEntries(t *testing.T) {
	var r Registry
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
		return []Attachment{nil, testAttachment{}, testAttachment{key: "attachment"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	open := func(context.Context) (*probe.Probe, error) {
		t.Fatal("classification opened hardware")
		return nil, nil
	}
	if err := r.RegisterProbe(NewProbeProvider("classifier", "usb", func(context.Context, Transport) ([]Candidate, error) {
		return []Candidate{NewCandidate(probe.Info{}, "", open), NewCandidate(probe.Info{}, "missing-callback", nil), NewCandidate(probe.Info{Serial: "valid"}, "valid", open)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	inventory, err := r.Transports(t.Context())
	if err == nil || strings.Count(err.Error(), "transport usb returned invalid attachment") != 2 {
		t.Fatalf("invalid attachment attribution: %v", err)
	}
	count := 0
	for entry := range inventory.All() {
		count++
		if entry.Info().Key != "attachment" {
			t.Fatal("invalid attachment survived")
		}
	}
	if count != 1 {
		t.Fatalf("valid attachments: %d", count)
	}
	candidates, err := inventory.Probes(t.Context())
	if err == nil || strings.Count(err.Error(), "probe classifier over usb at attachment returned invalid candidate") != 2 {
		t.Errorf("invalid candidate attribution: %v", err)
	}
	count = 0
	for candidate := range candidates {
		count++
		if candidate.Info().Serial != "valid" {
			t.Fatal("invalid candidate survived")
		}
	}
	if count != 1 {
		t.Fatalf("valid candidates: %d", count)
	}
}

func TestCombinedDiscoverySkipsTransportWithoutProbeProviders(t *testing.T) {
	var r Registry
	calls := 0
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
		calls++
		return []Attachment{testAttachment{key: "one"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Probes(t.Context()); !errors.Is(err, ErrNoProviders) {
		t.Fatalf("missing providers: %v", err)
	}
	if p, err := r.OpenProbe(t.Context(), Selection{}); p != nil || !errors.Is(err, ErrNoProviders) {
		t.Fatalf("missing providers open: %v", err)
	}
	if calls != 0 {
		t.Fatal("missing probe providers reached enumeration")
	}
}

func TestClassificationReportsCancellationOnceWithPartialCandidates(t *testing.T) {
	var r Registry
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) { return []Attachment{testAttachment{key: "one"}}, nil })); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(NewProbeProvider("a", "usb", func(context.Context, Transport) ([]Candidate, error) {
		cancel()
		return []Candidate{NewCandidate(probe.Info{Serial: "partial"}, "one", func(context.Context) (*probe.Probe, error) { t.Fatal("classification opened"); return nil, nil })}, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(NewProbeProvider("b", "usb", func(context.Context, Transport) ([]Candidate, error) {
		t.Fatal("classified after cancellation")
		return nil, nil
	})); err != nil {
		t.Fatal(err)
	}
	inventory, err := r.Transports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := inventory.Probes(ctx)
	if !errors.Is(err, context.Canceled) || strings.Count(err.Error(), context.Canceled.Error()) != 1 {
		t.Fatalf("repeated classification cancellation: %v", err)
	}
	candidate, err := candidates.Select(Selection{Serial: "partial"})
	if err != nil || candidate.Info().Serial != "partial" {
		t.Fatalf("lost accepted partial result: %v", err)
	}
}
