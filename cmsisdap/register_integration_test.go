//go:build integration

package cmsisdap_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/cmsisdap"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/target/cortexm"
)

func TestHILCMSISDAPSWDReadOnlyStateRestoration(t *testing.T) {
	if os.Getenv("OSTIOLE_CMSISDAP_TARGET_HIL") != "1" {
		t.Skip("OSTIOLE_CMSISDAP_TARGET_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	first := observeCMSISDAPTarget(t, ctx, false)
	second := observeCMSISDAPTarget(t, ctx, true)
	if first.dpidr != second.dpidr || first.cpuid != second.cpuid {
		t.Fatalf("identities changed across reopen: DPIDR %#08x/%#08x CPUID %#08x/%#08x", first.dpidr, second.dpidr, first.cpuid, second.cpuid)
	}
	const sHalt = uint32(1 << 17)
	if first.dhcsr&sHalt != second.dhcsr&sHalt {
		t.Fatalf("DHCSR.S_HALT changed across reopen: %#08x then %#08x", first.dhcsr, second.dhcsr)
	}
	t.Logf("DPIDR=%#08x AP0_IDR=%#08x CPUID=%#08x part=%#03x DHCSR=%#08x", first.dpidr, first.apidr, first.cpuid, first.part, first.dhcsr)
}

type cmsisdapTargetObservation struct {
	dpidr, apidr, cpuid, dhcsr uint32
	part                       uint16
}

func observeCMSISDAPTarget(t *testing.T, ctx context.Context, readyOnOpen bool) cmsisdapTargetObservation {
	t.Helper()
	var options []cmsisdap.Option
	if readyOnOpen {
		options = append(options, cmsisdap.WithSWD(1_000_000))
	}
	session := openCMSISDAPSession(t, ctx, options...)
	cleanup := newCMSISDAPCleanup(t)
	cleanup.retain("CMSIS-DAP session", func(context.Context) error { return session.Close() })
	if !readyOnOpen {
		if err := session.ConfigureSWD(ctx, 1_000_000); err != nil {
			t.Fatal(err)
		}
	}
	registers := cmsisdap.NewRegisterSWD(session)
	debugPort := dap.NewDebugPort(dap.RegisterDP(registers))
	cleanup.retain("debug port", debugPort.Release)

	identity, err := debugPort.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dpidr, apidr := confirmCMSISDAPTargetIdentities(t, ctx, debugPort, identity)
	savedCSW, err := debugPort.ReadRawAP(ctx, dap.NewAPSel(0).Address(0x00))
	if err != nil {
		t.Fatal(err)
	}
	savedTAR, err := debugPort.ReadRawAP(ctx, dap.NewAPSel(0).Address(0x04))
	if err != nil {
		t.Fatal(err)
	}
	memory, err := dap.OpenMemAP(ctx, debugPort, dap.NewAPSel(0))
	if err != nil {
		t.Fatal(err)
	}
	cleanup.retain("MEM-AP", memory.Release)
	core, err := cortexm.Identify(ctx, memory)
	if err != nil {
		t.Fatal(err)
	}
	dhcsr, err := memory.ReadWord(ctx, 0xe000edf0)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup.releaseCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	assertCMSISDAPAPRegister(t, ctx, debugPort, 0x00, savedCSW)
	assertCMSISDAPAPRegister(t, ctx, debugPort, 0x04, savedTAR)
	if err := cleanup.releaseCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cleanup.releaseCurrent(ctx); err != nil {
		t.Fatal(err)
	}

	return cmsisdapTargetObservation{
		dpidr: dpidr.Raw, apidr: apidr.Raw, cpuid: core.Raw, dhcsr: dhcsr,
		part: core.Part,
	}
}

func confirmCMSISDAPTargetIdentities(t *testing.T, ctx context.Context, debugPort *dap.DebugPort, identity dap.Identity) (dap.DPIDRInfo, dap.APIDRInfo) {
	t.Helper()
	dpidr, ok := identity.DPIDR()
	if !ok {
		t.Fatal("SW-DP identity has no DPIDR")
	}
	transaction := debugPort.NewTxn()
	dpidrResult := transaction.ReadDP(dap.DPIDR)
	apidrResult := transaction.ReadAPIDR(dap.NewAPSel(0))
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	confirmedDPIDR, err := dpidrResult.Value()
	if err != nil {
		t.Fatal(err)
	}
	apidrRaw, err := apidrResult.Value()
	if err != nil {
		t.Fatal(err)
	}
	apidr := dap.DecodeAPIDR(apidrRaw)
	if confirmedDPIDR != dpidr.Raw || apidr.Raw == 0 || apidr.Class != 8 {
		t.Fatalf("debug identities = DPIDR %#08x/%#08x AP0 %+v", dpidr.Raw, confirmedDPIDR, apidr)
	}
	return dpidr, apidr
}

func assertCMSISDAPAPRegister(t *testing.T, ctx context.Context, debugPort *dap.DebugPort, address uint8, want uint32) {
	t.Helper()
	got, err := debugPort.ReadRawAP(ctx, dap.NewAPSel(0).Address(uint16(address)))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("AP0 register %#02x = %#08x, want %#08x", address, got, want)
	}
}

type cmsisdapCleanupStep struct {
	name    string
	release func(context.Context) error
}

type cmsisdapCleanup struct {
	t     *testing.T
	steps []cmsisdapCleanupStep
}

func newCMSISDAPCleanup(t *testing.T) *cmsisdapCleanup {
	t.Helper()
	cleanup := &cmsisdapCleanup{t: t}
	t.Cleanup(cleanup.finish)
	return cleanup
}

func (c *cmsisdapCleanup) retain(name string, release func(context.Context) error) {
	c.steps = append(c.steps, cmsisdapCleanupStep{name: name, release: release})
}

func (c *cmsisdapCleanup) releaseCurrent(ctx context.Context) error {
	if len(c.steps) == 0 {
		return nil
	}
	step := c.steps[len(c.steps)-1]
	if err := step.release(ctx); err != nil {
		return fmt.Errorf("release %s: %w", step.name, err)
	}
	c.steps = c.steps[:len(c.steps)-1]
	return nil
}

func (c *cmsisdapCleanup) finish() {
	for len(c.steps) != 0 {
		var err, attempts error
		for range cmsisdapCloseAttempts {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = c.releaseCurrent(ctx)
			attempts = errors.Join(attempts, err)
			cancel()
			if err == nil {
				break
			}
		}
		if attempts != nil {
			c.t.Errorf("cleanup reported: %v", attempts)
		}
		if err != nil {
			c.t.Errorf("cleanup after %d attempts: %v", cmsisdapCloseAttempts, err)
			return
		}
	}
}
