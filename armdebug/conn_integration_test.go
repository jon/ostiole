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
	"github.com/jon/ostiole/coresight"
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

	if os.Getenv("OSTIOLE_ARMDEBUG_HIL_DEBUG_SPACE") == "1" {
		inspectDebugSpaceHIL(t, ctx, c.Port().DebugSpace())
	}
	if inspectMemory {
		inspectMemoryHIL(t, ctx, c, ap)
	}
}

func memorySelectionHIL(t *testing.T) (dap.APSel, bool) {
	t.Helper()
	if selected := os.Getenv("OSTIOLE_ARMDEBUG_HIL_AP_BASE"); selected != "" {
		if os.Getenv("OSTIOLE_ARMDEBUG_HIL_AP") != "" {
			t.Fatal("select either an AP index or base")
		}
		base, err := strconv.ParseUint(selected, 0, 64)
		if err != nil {
			t.Fatal(err)
		}
		sel, err := dap.APAt(base)
		if err != nil {
			t.Fatal(err)
		}
		return sel, true
	}
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
	base, present, err := memory.ReadDebugBase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s IDR=%#08x CPUID=%#08x debug_base=%#x present=%v", ap, id.Raw, processor.Raw, base, present)
	if present && os.Getenv("OSTIOLE_ARMDEBUG_HIL_WALK") == "1" {
		visits, err := coresight.Walk(ctx, memory, base, coresight.WalkLimits{MaxDepth: 8, MaxComponents: 64, MaxEntries: 256})
		for _, visit := range visits {
			t.Logf("ROM component=%+v error=%v", visit.Component, visit.Err)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
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

func inspectDebugSpaceHIL(t *testing.T, ctx context.Context, space dap.DebugSpace) {
	t.Helper()
	base, present, err := space.ReadDebugBase(ctx)
	if err != nil || !present {
		t.Fatalf("debug root=%#x,%v,%v", base, present, err)
	}
	visits, err := coresight.Walk(ctx, space, base, coresight.WalkLimits{MaxDepth: 8, MaxComponents: 64, MaxEntries: 256})
	for _, visit := range visits {
		if visit.Component != nil {
			t.Logf("debug-space base=%#x CIDR=%#08x PIDR=%#x DEVARCH=%#08x", visit.Component.Base, visit.Component.CIDR, visit.Component.PIDR, visit.Component.DEVARCH)
		}
		if visit.Err != nil {
			t.Log(visit.Err)
		}
	}
	t.Logf("debug-space visits=%d complete=%v", len(visits), err == nil)
	if err != nil {
		t.Fatal(err)
	}
}
