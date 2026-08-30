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
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/target/cortexm"
	"github.com/jon/ostiole/usb"
)

const cmsisdapCloseAttempts = 3

func TestHILCMSISDAPV1IsRejectedBeforeClaim(t *testing.T) {
	if os.Getenv("OSTIOLE_CMSISDAP_V1_HIL") != "1" {
		t.Skip("OSTIOLE_CMSISDAP_V1_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	device := openCMSISDAPUSBDevice(t, ctx)
	defer func() {
		closeCMSISDAPOwner(t, device, "CMSIS-DAP v1 USB device")
	}()

	session, err := cmsisdap.Open(ctx, device)
	if session != nil {
		closeCMSISDAPOwner(t, session, "unexpected CMSIS-DAP session")
		t.Fatalf("cmsisdap.Open() returned a session for CMSIS-DAP v1")
	}
	if !errors.Is(err, cmsisdap.ErrNoV2Interface) {
		t.Fatalf("cmsisdap.Open() error = %v, want ErrNoV2Interface", err)
	}
	identity := device.Identity()
	t.Logf("rejected CMSIS-DAP v1 attachment %04x:%04x product=%q before claiming an interface", identity.VID, identity.PID, identity.Product)
}

func TestHILCMSISDAPV2MetadataSurvivesReopen(t *testing.T) {
	if os.Getenv("OSTIOLE_CMSISDAP_HIL") != "1" {
		t.Skip("OSTIOLE_CMSISDAP_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	first := openCMSISDAPSession(t, ctx)
	firstInfo := first.Info()
	if firstInfo.ProtocolVersion == "" || firstInfo.PacketSize == 0 || firstInfo.PacketCount == 0 {
		t.Fatalf("incomplete CMSIS-DAP metadata = %#v", firstInfo)
	}
	if !closeCMSISDAPOwner(t, first, "first CMSIS-DAP session") {
		return
	}

	second := openCMSISDAPSession(t, ctx)
	defer func() {
		closeCMSISDAPOwner(t, second, "reopened CMSIS-DAP session")
	}()
	secondInfo := second.Info()
	if firstInfo.USB != secondInfo.USB || firstInfo.ProtocolVersion != secondInfo.ProtocolVersion || firstInfo.FirmwareVersion != secondInfo.FirmwareVersion {
		t.Fatalf("metadata changed across reopen: first %#v, second %#v", firstInfo, secondInfo)
	}
	t.Logf("CMSIS-DAP protocol=%q firmware=%q packet_size=%d packet_count=%d capabilities=%x", secondInfo.ProtocolVersion, secondInfo.FirmwareVersion, secondInfo.PacketSize, secondInfo.PacketCount, secondInfo.Capabilities.Bytes())
}

func TestHILCMSISDAPSWDReadOnlyStateRestoration(t *testing.T) {
	if os.Getenv("OSTIOLE_CMSISDAP_HIL") != "1" {
		t.Skip("OSTIOLE_CMSISDAP_HIL is not 1")
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
	if first.packedFrames < 2 || second.packedFrames < 2 {
		t.Fatalf("packed SWD frames = %d then %d", first.packedFrames, second.packedFrames)
	}
	t.Logf("DPIDR=%#08x AP0_IDR=%#08x CPUID=%#08x part=%#03x DHCSR=%#08x packed_frames=%d/%d SWDIO_calls=%d/%d", first.dpidr, first.apidr, first.cpuid, first.part, first.dhcsr, first.packedFrames, second.packedFrames, first.calls, second.calls)
}

type cmsisdapTargetObservation struct {
	dpidr, apidr, cpuid, dhcsr uint32
	part                       uint16
	calls, packedFrames        int
}

func observeCMSISDAPTarget(t *testing.T, ctx context.Context, readyOnOpen bool) cmsisdapTargetObservation {
	t.Helper()
	var options []cmsisdap.Option
	if readyOnOpen {
		options = append(options, cmsisdap.WithSWD(100_000))
	}
	session := openCMSISDAPSession(t, ctx, options...)
	cleanup := newCMSISDAPCleanup(t)
	cleanup.retain("CMSIS-DAP session", func(context.Context) error { return session.Close() })
	if !readyOnOpen {
		if err := session.ConfigureSWD(ctx, 100_000); err != nil {
			t.Fatal(err)
		}
	}
	wire := &cmsisdapRecordingWire{inner: session}
	debugPort := dap.NewDebugPort(swd.New(wire))
	cleanup.retain("debug port", debugPort.Release)

	dpidr, err := debugPort.Connect(ctx)
	if err != nil {
		for index, call := range wire.calls {
			t.Logf("SWDIO[%d] bits=%d direction=%x output=%x input=%x", index, call.bits, call.direction, call.output, call.input)
		}
		t.Fatal(err)
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

	packed := 0
	for _, call := range wire.calls {
		if call.bits > 54 && call.bits%54 == 0 {
			packed += call.bits / 54
		}
	}
	return cmsisdapTargetObservation{
		dpidr: dpidr.Raw, apidr: apidr.Raw, cpuid: core.Raw, dhcsr: dhcsr,
		part: core.Part, calls: len(wire.calls), packedFrames: packed,
	}
}

func assertCMSISDAPAPRegister(t *testing.T, ctx context.Context, debugPort *dap.DebugPort, address uint8, want uint32) {
	t.Helper()
	got, err := debugPort.ReadRawAP(ctx, dap.NewAPSel(0).Address(address))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("AP0 register %#02x = %#08x, want %#08x", address, got, want)
	}
}

type cmsisdapRecordingWire struct {
	inner *cmsisdap.Session
	calls []cmsisdapWireCall
}

type cmsisdapWireCall struct {
	direction, output, input []byte
	bits                     int
}

func (w *cmsisdapRecordingWire) MaxTransferBits() int { return w.inner.MaxTransferBits() }

func (w *cmsisdapRecordingWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	w.calls = append(w.calls, cmsisdapWireCall{
		direction: append([]byte(nil), direction...), output: append([]byte(nil), output...),
		input: append([]byte(nil), input...), bits: bits,
	})
	return input, err
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
		var err error
		for range cmsisdapCloseAttempts {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = c.releaseCurrent(ctx)
			cancel()
			if err == nil {
				break
			}
		}
		if err != nil {
			c.t.Errorf("cleanup after %d attempts: %v", cmsisdapCloseAttempts, err)
			c.steps = c.steps[:len(c.steps)-1]
		}
	}
}

func openCMSISDAPSession(t *testing.T, ctx context.Context, options ...cmsisdap.Option) *cmsisdap.Session {
	t.Helper()
	device := openCMSISDAPUSBDevice(t, ctx)
	session, err := cmsisdap.Open(ctx, device, options...)
	if err != nil {
		if session != nil {
			closeCMSISDAPOwner(t, session, "CMSIS-DAP session after failed open")
		} else {
			closeCMSISDAPOwner(t, device, "CMSIS-DAP USB device after failed open")
		}
		t.Fatal(err)
	}
	return session
}

func openCMSISDAPUSBDevice(t *testing.T, ctx context.Context) *usb.Device {
	t.Helper()
	enumerator := usb.New()
	devices, err := enumerator.List(ctx, []usb.DeviceFilter{usb.AllDevices()})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := selectCMSISDAPHILCandidate(devices, os.Getenv("OSTIOLE_CMSISDAP_HIL_DEVICE"), os.Getenv("OSTIOLE_CMSISDAP_HIL_SERIAL"))
	if errors.Is(err, errCMSISDAPHILUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	device, err := enumerator.Open(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func closeCMSISDAPOwner(t *testing.T, owner interface{ Close() error }, description string) bool {
	t.Helper()
	var err error
	for range cmsisdapCloseAttempts {
		if err = owner.Close(); err == nil {
			return true
		}
	}
	t.Errorf("close %s after %d attempts: %v", description, cmsisdapCloseAttempts, err)
	return false
}
