//go:build integration

package armdebug_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/discover"
	_ "github.com/jon/ostiole/discover/probes"
	"github.com/jon/ostiole/target/cortexm"
)

func TestHILArmConnection(t *testing.T) {
	if os.Getenv("OSTIOLE_ARMDEBUG_HIL") != "1" {
		t.Skip("OSTIOLE_ARMDEBUG_HIL is not 1")
	}
	selection := discover.Selection{
		Provider: discover.ProviderID(os.Getenv("OSTIOLE_PROBE_HIL_PROVIDER")),
		Serial:   os.Getenv("OSTIOLE_PROBE_HIL_SERIAL"),
		Function: os.Getenv("OSTIOLE_PROBE_HIL_FUNCTION"),
	}
	if selection.Provider == "" || selection.Serial == "" {
		t.Skip("explicit provider and serial required")
	}
	ap, inspectMemory := memorySelectionHIL(t)
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
	// Inventory preflight only decides whether the selected bench is available.
	// Open owns its discovery call and opens the exact binding once.
	c, err := armdebug.Open(ctx, discover.Selection{Binding: candidate.Info().Binding}, config())
	if c != nil {
		t.Cleanup(func() { closeHIL(t, c) })
	}
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Port().ReadDP(ctx, dap.DPIDR)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("provider=%s probe=%+v DPIDR=%#08x requested_clock_hz=100000", selection.Provider, c.Info(), id)
	if id>>12&15 == 3 {
		for _, reg := range []dap.DPRegister{dap.DPIDR1, dap.BASEPTR0, dap.BASEPTR1, dap.DPIDR} {
			value, err := c.Port().ReadDP(ctx, reg)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s=%#08x", reg, value)
		}
	}
	if inspectMemory {
		inspectMemoryHIL(t, ctx, c, ap)
	}
}

func memorySelectionHIL(t *testing.T) (dap.APSel, bool) {
	t.Helper()
	selected := os.Getenv("OSTIOLE_ARMDEBUG_HIL_AP")
	if selected == "" {
		return dap.APSel{}, false
	}
	index, err := strconv.ParseUint(selected, 10, 8)
	if err != nil {
		t.Fatal(err)
	}
	return dap.NewAPSel(uint8(index)), true
}

func inspectMemoryHIL(t *testing.T, ctx context.Context, c *armdebug.Conn, ap dap.APSel) {
	t.Helper()
	index, err := ap.Value()
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Port().ReadAPIDR(ctx, ap)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := c.OpenMemAP(ctx, ap)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := cortexm.Identify(ctx, memory)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AP%d IDR=%#08x CPUID=%#08x", index, id.Raw, processor.Raw)
}

func closeHIL(t *testing.T, c *armdebug.Conn) {
	t.Helper()
	var err error
	for range 3 {
		if err = c.Close(); err == nil {
			t.Log("Arm debug state released and probe closed")
			return
		}
	}
	t.Errorf("cleanup remains pending after three attempts: %v", err)
}
