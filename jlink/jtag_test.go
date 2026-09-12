package jlink

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jon/ostiole/jtag"
)

var _ jtag.Wire = (*Session)(nil)
var _ jtag.TransferLimits = (*Session)(nil)

func TestOpenWithJTAGSelectsInterfaceAndClock(t *testing.T) {
	operations := configuredSWDOperations(delayedInputFirmwareRecord, 100)
	operations[5].request = []byte{0xc7, 0}
	device := metadataPeer(t, operations)
	session, err := openSession(t.Context(), device, WithJTAG(100_999))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	if session.Info().SelectedInterface != 0 || session.ClockHz() != 100_000 || session.MaxTransferBits() != 504 || session.delayInput {
		t.Fatalf("JTAG configuration: %+v, clock %d, limit %d, delay %t", session.Info(), session.ClockHz(), session.MaxTransferBits(), session.delayInput)
	}
	if len(device.operations) != 0 {
		t.Fatal("configuration commands remain")
	}
	if _, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("SWD clocked JTAG interface")
	}
}

func TestJTAGScansClockEveryIndependentBit(t *testing.T) {
	for _, bits := range []int{1, 7, 8, 9, 63, 64, 65, 503, 504} {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			device := metadataPeer(t, nil)
			session := configuredSession(device, delayedInputFirmwareRecord)
			session.info.SelectedInterface = 0
			tms, tdi := make([]byte, (bits+7)/8), make([]byte, (bits+7)/8)
			want := make([]byte, len(tdi))
			for i := range bits {
				tms[i/8] |= byte((i/3)%2) << uint(i%8)
				tdi[i/8] |= byte((i/5+1)%2) << uint(i%8)
				want[i/8] |= byte((i/7+1)%2) << uint(i%8)
			}
			clocks := 0
			device.handleWrite = func(request []byte) (int, error) {
				if len(request) != 4+2*len(tdi) || request[0] != 0xcf || request[1] != 0 || int(binary.LittleEndian.Uint16(request[2:])) != bits {
					t.Fatalf("scan header: %x", request)
				}
				for i := range bits {
					ms := request[4+i/8] >> uint(i%8) & 1
					di := request[4+len(tdi)+i/8] >> uint(i%8) & 1
					if ms != byte((i/3)%2) || di != byte((i/5+1)%2) {
						t.Fatalf("clock %d: TMS %d TDI %d", i, ms, di)
					}
					clocks++
				}
				// Alternate split and coalesced completion layouts, including a ZLP.
				if bits%2 == 0 {
					device.responses = append(device.responses, append(append([]byte(nil), want...), 0))
				} else {
					device.responses = append(device.responses, want, nil, []byte{0})
				}
				return len(request), nil
			}
			got, err := session.JTAGIO(t.Context(), tms, tdi, bits)
			if err != nil || !bytes.Equal(got, want) || clocks != bits {
				t.Fatalf("scan: %x, %v, clocks %d", got, err, clocks)
			}
		})
	}
}

func TestJTAGFailuresPoisonWithoutReplay(t *testing.T) {
	want := errors.New("transfer failed")
	for _, phase := range []string{"command", "samples", "status", "surplus", "canceled"} {
		t.Run(phase, func(t *testing.T) {
			device := metadataPeer(t, []peerOperation{{request: []byte{0xcf, 0, 1, 0, 0, 0}, response: [][]byte{{0}, {0}}}})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch phase {
			case "command":
				device.writeErr = want
			case "samples":
				device.readErrs = []error{want}
			case "status":
				device.readErrs = []error{nil, want}
			case "surplus":
				device.operations[0].response = [][]byte{{0, 0, 1}}
			case "canceled":
				device.afterWrite = cancel
			}
			session := configuredSession(device, "probe")
			session.info.SelectedInterface = 0
			_, err := session.JTAGIO(ctx, []byte{0}, []byte{0}, 1)
			if !errors.Is(err, ErrSessionPoisoned) {
				t.Fatalf("not poisoned: %v", err)
			}
			if phase == "command" || phase == "samples" || phase == "status" {
				if !errors.Is(err, want) || !strings.Contains(err.Error(), "JTAG scan "+phase) {
					t.Fatalf("lost cause or phase: %v", err)
				}
			}
			if phase == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if _, err := session.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1); !errors.Is(err, ErrSessionPoisoned) {
				t.Fatalf("reused poisoned session: %v", err)
			}
			if device.writes != 1 {
				t.Fatal("scan was replayed")
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigureJTAGPreflightAndWorkspace(t *testing.T) {
	for _, test := range []struct {
		known           bool
		mask, workspace uint32
		want            int
	}{
		{false, 1, 132, 0}, {true, 2, 132, 0}, {true, 1, 5, 0}, {true, 1, 6, 8}, {true, 1, 132, 504},
	} {
		device := metadataPeer(t, []peerOperation{
			{request: []byte{0xc7, 0}, response: [][]byte{{1, 0, 0, 0}}}, {request: []byte{5, 100, 0}},
		})
		session := configuredSession(device, "probe")
		session.info.SelectedInterfaceKnown, session.info.AvailableInterfaces = test.known, test.mask
		session.info.WorkspaceKnown, session.info.Workspace = true, test.workspace
		err := session.ConfigureJTAG(t.Context(), 100_000)
		if test.want == 0 {
			if err == nil || device.writes != 0 || !session.configured {
				t.Fatalf("preflight: %v writes %d", err, device.writes)
			}
			continue
		}
		if err != nil || session.MaxTransferBits() != test.want {
			t.Fatalf("limit: %d %v", session.MaxTransferBits(), err)
		}
		if test.want == 8 {
			if _, err := session.JTAGIO(t.Context(), []byte{0, 0}, []byte{0, 0}, 9); err == nil || device.writes != 2 {
				t.Fatal("workspace limit ignored")
			}
		}
	}
}

func TestJTAGCloseInvalidatesScansEvenWhenReleaseFails(t *testing.T) {
	device := metadataPeer(t, nil)
	device.releaseErr = errors.New("release failed")
	session := configuredSession(device, "probe")
	session.info.SelectedInterface = 0
	if err := session.Close(); !errors.Is(err, device.releaseErr) {
		t.Fatalf("close: %v", err)
	}
	if _, err := session.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil || session.ClockHz() != 0 || session.MaxTransferBits() != 0 {
		t.Fatal("closed wire still usable")
	}
	if err := session.ConfigureJTAG(t.Context(), 100_000); err == nil {
		t.Fatal("reconfigured during cleanup")
	}
	device.releaseErr = nil
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if device.writes != 0 || device.closes != 1 {
		t.Fatalf("cleanup: %d writes %d closes", device.writes, device.closes)
	}
}

func TestJTAGAndSWDReconfigurationResetsSampleState(t *testing.T) {
	device := metadataPeer(t, []peerOperation{
		{request: []byte{0xc7, 0}, response: [][]byte{{1, 0, 0, 0}}}, {request: []byte{5, 100, 0}},
		{request: []byte{0xc7, 1}, response: [][]byte{{0, 0, 0, 0}}}, {request: []byte{5, 200, 0}},
	})
	session := configuredSession(device, delayedInputFirmwareRecord)
	session.info.SelectedInterfaceKnown, session.info.AvailableInterfaces = true, 3
	session.inputCarry = true
	if err := session.ConfigureJTAG(t.Context(), 100_000); err != nil {
		t.Fatal(err)
	}
	if session.delayInput || session.inputCarry {
		t.Fatal("JTAG inherited SWD sample state")
	}
	if _, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("SWD accepted JTAG selection")
	}
	if err := session.ConfigureSWD(t.Context(), 200_000); err != nil {
		t.Fatal(err)
	}
	if !session.delayInput || session.inputCarry || session.ClockHz() != 200_000 {
		t.Fatal("SWD configuration not restored")
	}
	if _, err := session.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("JTAG accepted SWD selection")
	}
	if device.writes != 4 {
		t.Fatal("mode checks clocked hardware")
	}
}

func TestJTAGConfigurationFailureInvalidatesBothWires(t *testing.T) {
	for _, previous := range []byte{1, 32} {
		device := metadataPeer(t, []peerOperation{{request: []byte{0xc7, 0}, response: [][]byte{{previous, 0, 0, 0}}}})
		session := configuredSession(device, "probe")
		session.info.SelectedInterfaceKnown, session.info.AvailableInterfaces = true, 3
		if previous == 1 {
			device.afterWrite = func() { device.writeErr = errors.New("clock failed") }
		}
		if err := session.ConfigureJTAG(t.Context(), 100_000); err == nil {
			t.Fatal("failed configuration succeeded")
		}
		writes := device.writes
		if session.MaxTransferBits() != 0 {
			t.Fatal("failed configuration has transfer limit")
		}
		if _, err := session.SWDIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
			t.Fatal("SWD active after failure")
		}
		if _, err := session.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
			t.Fatal("JTAG active after failure")
		}
		if writes != device.writes {
			t.Fatal("failed configuration sent more traffic")
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJTAGOptionsRejectConflictingModesBeforeTraffic(t *testing.T) {
	for _, options := range [][]Option{
		{WithJTAG(999)}, {WithSWD(100_000), WithJTAG(100_000)}, {WithJTAG(100_000), WithSWD(100_000)},
	} {
		device := metadataPeer(t, nil)
		if _, err := openSession(t.Context(), device, options...); err == nil {
			t.Fatal("invalid options accepted")
		}
		if device.configurationN != 0 || device.writes != 0 {
			t.Fatal("invalid options reached hardware")
		}
	}
}

func TestJTAGIOPreservesIndependentStreamsAndSamples(t *testing.T) {
	device := metadataPeer(t, []peerOperation{{
		request: []byte{0xcf, 0, 9, 0, 0x55, 1, 0xaa, 1}, response: [][]byte{{0xad, 0xff, 0}},
	}})
	session := configuredSession(device, delayedInputFirmwareRecord)
	session.info.SelectedInterface = 0
	tms, tdi := []byte{0x55, 0xff}, []byte{0xaa, 0xff}
	got, err := session.JTAGIO(t.Context(), tms, tdi, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{0xad, 1}) || tms[1] != 0xff || tdi[1] != 0xff {
		t.Fatalf("samples %x, inputs %x/%x", got, tms, tdi)
	}
}

func TestJTAGPreflightDoesNotClock(t *testing.T) {
	device := metadataPeer(t, nil)
	session := configuredSession(device, "probe")
	session.info.SelectedInterface = 0
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		ctx  context.Context
		bits int
	}{
		{t.Context(), -1}, {t.Context(), 505}, {t.Context(), 9}, {nil, 1}, {canceled, 1},
	} {
		if _, err := session.JTAGIO(test.ctx, []byte{0}, []byte{0}, test.bits); err == nil {
			t.Fatal("invalid scan accepted")
		}
	}
	if got, err := session.JTAGIO(t.Context(), nil, nil, 0); err != nil || len(got) != 0 {
		t.Fatalf("empty scan: %x %v", got, err)
	}
	session.info.SelectedInterface = 1
	if _, err := session.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("JTAG clocked SWD interface")
	}
	if device.writes != 0 {
		t.Fatal("preflight sent traffic")
	}
}

func TestJTAGScanStatusRequiresReconfiguration(t *testing.T) {
	device := metadataPeer(t, []peerOperation{
		{request: []byte{0xcf, 0, 1, 0, 0, 0}, response: [][]byte{{0}, {6}}},
		{request: []byte{0xc7, 0}, response: [][]byte{{0, 0, 0, 0}}},
		{request: []byte{0x05, 100, 0}},
	})
	session := configuredSession(device, "probe")
	session.info.SelectedInterface = 0
	session.info.SelectedInterfaceKnown = true
	session.info.AvailableInterfaces = 1
	_, err := session.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1)
	var status *ScanError
	if !errors.As(err, &status) || status.Status != 6 || session.poisoned || session.MaxTransferBits() != 0 {
		t.Fatalf("scan failure: %v", err)
	}
	if _, err := session.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("scan accepted after failure")
	}
	if err := session.ConfigureJTAG(t.Context(), 100_000); err != nil {
		t.Fatal(err)
	}
	if len(device.operations) != 0 || session.MaxTransferBits() != 504 {
		t.Fatal("reconfiguration incomplete")
	}
}
