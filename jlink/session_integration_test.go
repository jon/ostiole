//go:build integration

package jlink_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/jlink"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/target/cortexm"
	"github.com/jon/ostiole/usb"
)

const cortexMDHCSR = uint32(0xe000edf0)

func TestHILJLinkMetadataSurvivesReopen(t *testing.T) {
	if os.Getenv("OSTIOLE_JLINK_HIL") != "1" {
		t.Skip("OSTIOLE_JLINK_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	first := openMetadataSession(t, ctx)
	defer func() {
		closeJLinkOwner(t, first, "first J-Link session")
	}()
	firstInfo := first.Info()
	if firstInfo.Firmware == "" || firstInfo.Capabilities.BitLen() < 32 {
		t.Fatalf("incomplete J-Link metadata = %#v", firstInfo)
	}
	if !closeJLinkOwner(t, first, "first J-Link session") {
		return
	}

	second := openMetadataSession(t, ctx)
	defer func() {
		closeJLinkOwner(t, second, "reopened J-Link session")
	}()
	secondInfo := second.Info()
	if firstInfo.USB.VID != secondInfo.USB.VID || firstInfo.USB.PID != secondInfo.USB.PID || firstInfo.Firmware != secondInfo.Firmware {
		t.Fatalf("metadata changed across reopen: first %#v, second %#v", firstInfo, secondInfo)
	}
	if firstInfo.SelectedInterfaceKnown != secondInfo.SelectedInterfaceKnown || firstInfo.SelectedInterfaceKnown && firstInfo.SelectedInterface != secondInfo.SelectedInterface {
		t.Fatalf("selected interface changed across metadata-only reopen: first %d/%t, second %d/%t", firstInfo.SelectedInterface, firstInfo.SelectedInterfaceKnown, secondInfo.SelectedInterface, secondInfo.SelectedInterfaceKnown)
	}
	t.Logf("J-Link firmware=%q hardware=%#08x capabilities=%d workspace=%d/%t interfaces=%#08x selected=%d/%t", secondInfo.Firmware, secondInfo.Hardware.Raw, secondInfo.Capabilities.BitLen(), secondInfo.Workspace, secondInfo.WorkspaceKnown, secondInfo.AvailableInterfaces, secondInfo.SelectedInterface, secondInfo.SelectedInterfaceKnown)
}

func TestHILJLinkSWDDPIDR(t *testing.T) {
	if os.Getenv("OSTIOLE_JLINK_HIL") != "1" {
		t.Skip("OSTIOLE_JLINK_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	session := openJLinkSession(t, ctx, jlink.WithSWD(100_000))
	cleanup := retainJLinkSession(t, session)
	recorder := &recordingWire{inner: session}
	connection := swd.New(recorder)
	cleanup.retain("SWD connection", true, connection.Release)
	raw, err := connection.Connect(ctx)
	if err != nil {
		for index, call := range recorder.calls {
			t.Logf("SWDIO[%d] bits=%d direction=%x output=%x input=%x", index, call.bits, call.direction, call.output, call.input)
		}
		t.Fatal(err)
	}
	info, err := dap.DecodeDPIDR(raw)
	if err != nil {
		t.Fatal(err)
	}
	if session.ClockHz() != 100_000 || session.MaxTransferBits() != 504 {
		t.Fatalf("J-Link SWD configuration = %d Hz, %d bits", session.ClockHz(), session.MaxTransferBits())
	}
	t.Logf("DPIDR=%#08x version=%d designer=%#03x clock=%d max_bits=%d", raw, info.Version, info.Designer, session.ClockHz(), session.MaxTransferBits())
}

func TestHILJLinkSWDReadOnlyStateRestoration(t *testing.T) {
	if os.Getenv("OSTIOLE_JLINK_HIL") != "1" {
		t.Skip("OSTIOLE_JLINK_HIL is not 1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	first := observeJLinkTarget(t, ctx, false)
	second := observeJLinkTarget(t, ctx, true)
	if second.dpidr != first.dpidr {
		t.Fatalf("DPIDR across reopen = %#08x then %#08x", first.dpidr, second.dpidr)
	}
	if first.cpuid != second.cpuid {
		t.Fatalf("CPUID across reopen = %#08x then %#08x", first.cpuid, second.cpuid)
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

type targetObservation struct {
	dpidr, apidr, cpuid, dhcsr uint32
	part                       uint16
	calls, packedFrames        int
}

func observeJLinkTarget(t *testing.T, ctx context.Context, readyOnOpen bool) targetObservation {
	t.Helper()
	var options []jlink.Option
	if readyOnOpen {
		options = append(options, jlink.WithSWD(100_000))
	}
	session := openJLinkSession(t, ctx, options...)
	cleanup := retainJLinkSession(t, session)
	if !readyOnOpen {
		if err := session.ConfigureSWD(ctx, 100_000); err != nil {
			t.Fatal(err)
		}
	}
	recorder := &recordingWire{inner: session}
	debugPort := dap.NewDebugPort(swd.New(recorder))
	cleanup.retain("debug port", true, debugPort.Release)

	dpidr, err := debugPort.Connect(ctx)
	if err != nil {
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
	cleanup.retain("MEM-AP", true, memory.Release)
	core, err := cortexm.Identify(ctx, memory)
	if err != nil {
		t.Fatal(err)
	}
	dhcsr, err := memory.ReadWord(ctx, cortexMDHCSR)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup.releaseCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	assertAPRegister(t, ctx, debugPort, 0x00, savedCSW)
	assertAPRegister(t, ctx, debugPort, 0x04, savedTAR)
	if err := cleanup.releaseCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cleanup.releaseCurrent(ctx); err != nil {
		t.Error(err)
		return targetObservation{}
	}

	packed := 0
	for _, call := range recorder.calls {
		if call.bits > 54 && call.bits%54 == 0 {
			packed += call.bits / 54
		}
	}
	return targetObservation{
		dpidr: dpidr.Raw, apidr: apidr.Raw, cpuid: core.Raw, dhcsr: dhcsr,
		part: core.Part, calls: len(recorder.calls), packedFrames: packed,
	}
}

func assertAPRegister(t *testing.T, ctx context.Context, debugPort *dap.DebugPort, address uint8, want uint32) {
	t.Helper()
	got, err := debugPort.ReadRawAP(ctx, dap.NewAPSel(0).Address(address))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("AP0 register %#02x = %#08x, want %#08x", address, got, want)
	}
}

type wireCall struct {
	direction, output, input []byte
	bits                     int
}

type recordingWire struct {
	inner *jlink.Session
	calls []wireCall
}

func (w *recordingWire) MaxTransferBits() int { return w.inner.MaxTransferBits() }

func (w *recordingWire) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	input, err := w.inner.SWDIO(ctx, direction, output, bits)
	w.calls = append(w.calls, wireCall{
		direction: append([]byte(nil), direction...), output: append([]byte(nil), output...),
		input: append([]byte(nil), input...), bits: bits,
	})
	return input, err
}

func openMetadataSession(t *testing.T, ctx context.Context) *jlink.Session {
	return openJLinkSession(t, ctx)
}

func openJLinkSession(t *testing.T, ctx context.Context, options ...jlink.Option) *jlink.Session {
	t.Helper()
	enumerator := usb.New()
	candidates, err := enumerator.List(ctx, jlink.SupportedDevices())
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := selectJLinkHILCandidate(candidates, os.Getenv("OSTIOLE_JLINK_HIL_DEVICE"))
	if err != nil {
		t.Fatal(err)
	}
	device, err := enumerator.Open(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	session, err := jlink.Open(ctx, device, options...)
	if err != nil {
		closeJLinkOwner(t, device, "J-Link USB device after failed open")
		t.Fatal(err)
	}
	return session
}

func retainJLinkSession(t *testing.T, session *jlink.Session) *jlinkCleanup {
	t.Helper()
	cleanup := newJLinkCleanup(func() bool { return session.ClockHz() != 0 }, func(ctx context.Context) error {
		return session.ConfigureSWD(ctx, 100_000)
	})
	cleanup.retain("J-Link session", false, func(context.Context) error { return session.Close() })
	t.Cleanup(func() { releaseJLinkCleanup(t, cleanup) })
	return cleanup
}
