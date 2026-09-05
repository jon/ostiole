//go:build integration

package probes_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/swd"
)

func TestHILProbeInventory(t *testing.T) {
	if os.Getenv("OSTIOLE_PROBE_HIL") != "1" {
		t.Skip("OSTIOLE_PROBE_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	inventory, err := discover.Probes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for candidate := range inventory {
		t.Logf("%+v", candidate.Info())
	}
}

func TestHILDiscoveredProbeDPIDR(t *testing.T) {
	if os.Getenv("OSTIOLE_PROBE_HIL") != "1" {
		t.Skip("OSTIOLE_PROBE_HIL is not 1")
	}
	selection := discover.Selection{
		Provider: discover.ProviderID(os.Getenv("OSTIOLE_PROBE_HIL_PROVIDER")),
		Serial:   os.Getenv("OSTIOLE_PROBE_HIL_SERIAL"),
		Function: os.Getenv("OSTIOLE_PROBE_HIL_FUNCTION"),
	}
	if selection.Provider == "" || selection.Serial == "" {
		t.Skip("explicit provider and serial required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	inventory, err := discover.Probes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := inventory.Select(selection)
	if errors.Is(err, discover.ErrCandidateNotFound) || errors.Is(err, discover.ErrCandidateAmbiguous) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	owner, err := candidate.Open(ctx)
	if owner != nil {
		t.Cleanup(func() { retryProbeCleanup(t, owner.Close) })
	}
	if err != nil {
		t.Fatal(err)
	}
	wire, err := owner.SWD(ctx, probe.SWDConfig{MaxClockHz: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	connection := swd.New(wire)
	t.Cleanup(func() {
		retryProbeCleanup(t, func() error {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
			defer cleanupCancel()
			return connection.Release(cleanupCtx)
		})
	})
	raw, err := connection.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("provider=%s serial=%q function=%q product=%q DPIDR=%#08x max_bits=%d",
		selection.Provider, selection.Serial, selection.Function, owner.Info().Product, raw, wire.MaxTransferBits())
}

func retryProbeCleanup(t *testing.T, close func() error) {
	t.Helper()
	var err error
	for range 3 {
		if err = close(); err == nil {
			return
		}
	}
	t.Errorf("cleanup after three attempts: %v", err)
}
