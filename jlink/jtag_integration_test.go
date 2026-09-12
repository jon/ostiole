//go:build integration

package jlink_test

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/jlink"
	jlinkdiscovery "github.com/jon/ostiole/jlink/discovery"
	"github.com/jon/ostiole/jtag"
	"github.com/jon/ostiole/probe"
)

func jtagHILContext(t *testing.T) context.Context {
	t.Helper()
	if os.Getenv("OSTIOLE_JLINK_JTAG_HIL") != "1" || os.Getenv("OSTIOLE_JLINK_HIL_SERIAL") == "" {
		t.Skip("set OSTIOLE_JLINK_JTAG_HIL=1 and OSTIOLE_JLINK_HIL_SERIAL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestHILJLinkJTAG(t *testing.T) {
	ctx := jtagHILContext(t)
	session := openJLinkSession(t, ctx, jlink.WithJTAG(100_000))
	released := true
	defer func() {
		if !released {
			t.Error("retaining session after failed chain cleanup")
			return
		}
		closeJLinkOwner(t, session, "JTAG session")
	}()
	t.Logf("J-Link serial=%s firmware=%q clock=%d limit=%d", session.Info().USB.Serial, session.Info().Firmware, session.ClockHz(), session.MaxTransferBits())
	exerciseJLinkJTAG(t, ctx, session, &released)
}

func TestHILJLinkProbeJTAG(t *testing.T) {
	ctx := jtagHILContext(t)
	var registry discover.Registry
	if err := jlinkdiscovery.Register(&registry); err != nil {
		t.Fatal(err)
	}
	inventory, err := registry.Probes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := inventory.Select(discover.Selection{Provider: jlinkdiscovery.ID, Serial: os.Getenv("OSTIOLE_JLINK_HIL_SERIAL")})
	if errors.Is(err, discover.ErrCandidateNotFound) || errors.Is(err, discover.ErrCandidateAmbiguous) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	owner, err := candidate.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := true
	defer func() {
		if !released {
			t.Error("retaining probe after failed chain cleanup")
			return
		}
		closeJLinkOwner(t, owner, "JTAG probe")
	}()
	wire, err := owner.JTAG(ctx, probe.JTAGConfig{MaxClockHz: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	exerciseJLinkJTAG(t, ctx, wire, &released)
	if !released {
		return
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.JTAGIO(ctx, []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("borrowed wire survived close")
	}
}

func exerciseJLinkJTAG(t *testing.T, ctx context.Context, wire jtag.Wire, released *bool) {
	t.Helper()
	conn := jtag.New(wire)
	part, err := jtag.IDCODE(5, 0x120034e5)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := jtag.NewChain(conn, jtag.Layout{part, part})
	if err != nil {
		t.Fatal(err)
	}
	*released = false
	defer func() {
		for range 3 {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = chain.Release(cleanup)
			cancel()
			if err == nil {
				*released = true
				t.Log("chain released to BYPASS/Idle")
				return
			}
		}
		t.Errorf("chain cleanup failed: %v", err)
	}()
	entries, err := conn.Discover(ctx, 8)
	if err != nil {
		t.Fatalf("discover: %v; entries %+v", err, entries)
	}
	for i, entry := range entries {
		t.Logf("TAP%d IDCODE=%#08x bypass=%t", i, entry.IDCODE, entry.Bypass)
	}
	bits, err := conn.MeasureIR(ctx, 128)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("total IR length: %d", bits)
	if err := chain.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		tap, err := chain.TAP(i)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tap.ScanIR(ctx, []byte{0x1e}); err != nil {
			t.Fatal(err)
		}
		data, err := tap.ScanDR(ctx, make([]byte, 4), 32)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != 4 || binary.LittleEndian.Uint32(data) != 0x120034e5 {
			t.Fatalf("TAP%d selected IDCODE: %x", i, data)
		}
		t.Logf("TAP%d selected IDCODE=%#08x; other TAP bypassed", i, binary.LittleEndian.Uint32(data))
	}
}
